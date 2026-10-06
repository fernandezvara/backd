// backd's function runner: `deno run <permissions> runner.js <bundle>`, one
// process per invocation, started by `backd executor`.
//
// Protocol: stdout carries only the protocol — {"ready":true} once the
// bundle is loaded, then one JSON result line. stdin carries one JSON
// envelope (input, caller, secrets, callback). The function's console goes
// to stderr as JSON lines, where the executor captures and masks it.
import { createClient } from "./client/index.js";
import { createFlusher, createSteps } from "./steps.js";

const enc = new TextEncoder();
const stdoutWrite = Deno.stdout.writeSync.bind(Deno.stdout);
const stderrWrite = Deno.stderr.writeSync.bind(Deno.stderr);
const out = (obj) => stdoutWrite(enc.encode(JSON.stringify(obj) + "\n"));

// Function code gets stderr as its stdout, so it can't write to (or forge)
// the protocol.
Object.defineProperty(Deno, "stdout", { value: Deno.stderr, writable: false, configurable: false });

const text = (a) => {
  if (typeof a === "string") return a;
  if (a instanceof Error) return a.stack ?? String(a);
  try { return JSON.stringify(a); } catch { return String(a); }
};
for (const level of ["log", "info", "warn", "error", "debug", "trace", "dir", "table"]) {
  const value = (...args) => stderrWrite(enc.encode(JSON.stringify({ level, line: args.map(text).join(" ") }) + "\n"));
  Object.defineProperty(console, level, { value, writable: false, configurable: false });
}

// A 4xx error the function wants its caller to see, as `throw ctx.error(…)`.
const brand = Symbol.for("backd.FunctionError");
class FunctionError extends Error {
  constructor(status, code, message, details) {
    super(message);
    this.name = "FunctionError";
    this.status = status;
    this.code = code;
    this.details = details;
    this[brand] = true;
  }
}

let handler;
try {
  const mod = await import(new URL(Deno.args[0], "file:///").href);
  handler = mod.default;
  if (typeof handler !== "function") throw new Error("the bundle's default export is not a function");
} catch (e) {
  out({ ok: false, error: `load: ${e?.stack ?? e}` });
  Deno.exit(1);
}
out({ ready: true });

// One envelope per process.
let buf = "";
const dec = new TextDecoder();
for await (const chunk of Deno.stdin.readable) {
  buf += dec.decode(chunk, { stream: true });
  if (buf.includes("\n")) break;
}
const env = JSON.parse(buf.slice(0, buf.indexOf("\n")));

const client = (token) =>
  createClient({ url: env.callback.url, realm: env.callback.realm, apiKey: token, headers: { "X-Request-ID": env.request_id ?? "" } });

const ctx = {
  input: env.input ?? null,
  user: env.user ?? null,
  secrets: Object.freeze({ ...(env.secrets ?? {}) }),
  idempotencyKey: env.idempotency_key ?? null,
  requestId: env.request_id ?? null,
  error: (status, code, message, details) => {
    if (!Number.isInteger(status) || status < 400 || status > 499) {
      throw new TypeError("ctx.error: status must be a 4xx code");
    }
    return new FunctionError(status, String(code), String(message ?? code), details);
  },
};
if (env.mode === "webhook") {
  // backd sends headers as {Name: [values...]} (Go's http.Header, once
  // JSON-encoded); functions get plain lower-case-keyed strings, the
  // shape signature-verification examples (Stripe's included) expect.
  // Several values for the same header are folded with ", ", as HTTP
  // itself treats them as equivalent to one comma-joined value.
  const headers = {};
  for (const [k, v] of Object.entries(env.webhook?.headers ?? {})) {
    headers[k.toLowerCase()] = Array.isArray(v) ? v.join(", ") : String(v);
  }
  ctx.request = Object.freeze({
    body: env.webhook?.body ?? "",
    headers: Object.freeze(headers),
  });
}
if (env.callback?.token) {
  const caller = client(env.callback.token);
  ctx.db = (name) => caller.db(name);
  // Calls another function of this database that function.yaml lists in
  // `calls`: its output for a sync callee, a job handle for an async one.
  // A 4xx the callee chose (ctx.error) arrives as the same FunctionError;
  // anything else (function_failed, function_timeout, call_not_declared,
  // unavailable) is a BackdError the caller may catch or let end it.
  ctx.call = async (name, input, options = {}) => {
    try {
      const opts = options.idempotencyKey === undefined ? undefined : { idempotencyKey: options.idempotencyKey };
      return await caller.db(env.callback.database).fn(name, input, opts);
    } catch (e) {
      if (e && Number.isInteger(e.status) && e.status >= 400 && e.status <= 499 && typeof e.code === "string"
          && e.code !== "call_not_declared" && e.code !== "call_too_deep") {
        throw new FunctionError(e.status, e.code, e.message, e.details?.length ? e.details : undefined);
      }
      throw e;
    }
  };
}
if (env.callback?.token && env.callback.email) {
  // Sends a custom email through the realm's templates, delivery function and
  // limits: kind (a template of the realm), data (for the template), and who
  // gets it: to_user (a realm user's id) or to / cc / bcc (addresses). Returns
  // { id } of the email's job. A limit (a function, an invocation, a recipient)
  // throws an EmailLimitError with code "email_limited" and retry_after.
  const sender = client(env.callback.token);
  ctx.email = Object.freeze({
    send: async (message) => {
      try {
        const { data } = await sender.request({ method: "POST", path: ["_email", "send"], body: message });
        return data;
      } catch (e) {
        if (e && e.status === 429) {
          const err = new Error(e.message);
          err.name = "EmailLimitError";
          err.code = "email_limited";
          err.retry_after = e.retryAfter === undefined ? null : Math.ceil(e.retryAfter / 1000);
          throw err;
        }
        throw e;
      }
    },
  });
}
if (env.callback?.admin_token) {
  const admin = client(env.callback.admin_token);
  ctx.admin = Object.freeze({ db: (name) => admin.db(name) });
}
// Steps and progress: kept here and sent with the result; for an async job
// backd also takes them live (coalesced), so a running job shows how far it got.
let flusher = null;
const steps = createSteps({ onChange: (kind) => flusher?.changed(kind) });
if (env.callback?.token && env.callback.progress) {
  const reporter = client(env.callback.token);
  flusher = createFlusher({ steps, send: (body) => reporter.request({ method: "PUT", path: ["_job", "steps"], body }) });
}
ctx.step = (name, options) => steps.step(name, options);
ctx.progress = (current, message) => steps.progress(current, message);
Object.freeze(ctx);

// The result line carries the steps (closed as done or failed) when there are any.
const ending = async (status, line) => {
  if (!steps.started) return line;
  steps.finish(status);
  if (flusher) await Promise.race([flusher.flush(), new Promise((r) => setTimeout(r, 2000))]);
  const { steps: list, omitted } = steps.snapshot();
  return { ...line, steps: list, steps_omitted: omitted };
};

try {
  const result = await handler(ctx);
  if (env.mode === "webhook") {
    const status = Number.isInteger(result?.status) ? result.status : 200;
    const body = typeof result?.body === "string" ? result.body : "";
    const headers = result?.headers && typeof result.headers === "object" ? result.headers : {};
    out(await ending("done", { ok: true, webhook: { status, body, headers } }));
  } else {
    out(await ending("done", { ok: true, output: result === undefined ? null : result }));
  }
  Deno.exit(0);
} catch (e) {
  if (e && e[brand]) {
    out(await ending("failed", { ok: false, function_error: { status: e.status, code: e.code, message: e.message, details: e.details ?? null } }));
  } else {
    out(await ending("failed", { ok: false, error: String(e?.stack ?? e) }));
  }
  Deno.exit(1);
}
