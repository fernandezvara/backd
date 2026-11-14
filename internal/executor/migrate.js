// backd's migration runner: `deno run --allow-read=<bundle> migrate.js <bundle>`, one process per
// batch of documents, started by `backd-server executor` (design/schema-versioning.md §5.2, §8.2).
//
// A migration is pure and synchronous, so this process has no permissions but reading its
// bundle (no network, no environment) and no clock or randomness: they throw. Protocol as
// runner.js: stdout carries {"ready":true}, then one JSON result line; stdin carries one JSON
// envelope, here {input: {current, docs: [{id, version, schema_version, meta, fields}]}}.
// The result is {ok: true, output: [{id, fields} | {id, skipped: true} | {id, error: {...}}]}.
// console goes to stderr as JSON lines, where the executor captures and masks it.

const enc = new TextEncoder();
const stdoutWrite = Deno.stdout.writeSync.bind(Deno.stdout);
const stderrWrite = Deno.stderr.writeSync.bind(Deno.stderr);
const out = (obj) => stdoutWrite(enc.encode(JSON.stringify(obj) + "\n"));

// Migration code gets stderr as its stdout, so it can't write to (or forge) the protocol.
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

// The same input must give the same output, because the job may run a document more than once:
// the clock and randomness are unavailable. Converting a stored date (new Date(value),
// Date.parse) is legitimate and still works.
const NONDETERMINISTIC = "a migration must be deterministic: it can't read the clock or randomness";
const refuse = () => {
  throw new Error(NONDETERMINISTIC);
};
const RealDate = Date;
function GuardedDate(...args) {
  if (!new.target || args.length === 0) refuse();
  return Reflect.construct(RealDate, args, new.target === GuardedDate ? RealDate : new.target);
}
GuardedDate.prototype = RealDate.prototype;
Object.setPrototypeOf(GuardedDate, RealDate);
Object.defineProperty(GuardedDate, "now", { value: refuse, writable: false, configurable: false });
Object.defineProperty(RealDate.prototype, "constructor", { value: GuardedDate, writable: true, configurable: true });
Object.defineProperty(globalThis, "Date", { value: GuardedDate, writable: false, configurable: false });
Object.defineProperty(performance, "now", { value: refuse, writable: false, configurable: false });
Object.defineProperty(Math, "random", { value: refuse, writable: false, configurable: false });
for (const name of ["getRandomValues", "randomUUID"]) {
  Object.defineProperty(Crypto.prototype, name, { value: refuse, writable: false, configurable: false });
  Object.defineProperty(crypto, name, { value: refuse, writable: false, configurable: false });
}

let steps;
try {
  const mod = await import(new URL(Deno.args[0], "file:///").href);
  steps = mod.steps;
  if (!Array.isArray(steps) || steps.some((s) => !Number.isInteger(s?.to) || typeof s?.run !== "function")) {
    throw new Error("the bundle's `steps` export is not a list of {to, run}");
  }
  steps = [...steps].sort((a, b) => a.to - b.to);
} catch (e) {
  out({ ok: false, error: `load: ${e?.stack ?? e}` });
  Deno.exit(1);
}
out({ ready: true });

let buf = "";
const dec = new TextDecoder();
for await (const chunk of Deno.stdin.readable) {
  buf += dec.decode(chunk, { stream: true });
  if (buf.includes("\n")) break;
}
const env = JSON.parse(buf.slice(0, buf.indexOf("\n")));
const { current, docs } = env.input ?? {};

const deepFreeze = (v) => {
  if (v !== null && typeof v === "object" && !Object.isFrozen(v)) {
    Object.freeze(v);
    for (const k of Object.keys(v)) deepFreeze(v[k]);
  }
  return v;
};

// The result must survive a JSON round trip: no function, symbol, bigint, NaN or infinity anywhere,
// and an object at the top. (undefined properties are dropped by JSON itself.)
const checkJSON = (v, path) => {
  switch (typeof v) {
    case "function": case "symbol": case "bigint":
      return `${path || "result"} is a ${typeof v}`;
    case "number":
      return Number.isFinite(v) ? null : `${path || "result"} is ${v}`;
    case "object": {
      if (v === null) return null;
      if (v instanceof RealDate) return null; // JSON turns it into its ISO string, as the API does
      for (const k of Object.keys(v)) {
        const bad = checkJSON(v[k], path ? `${path}.${k}` : k);
        if (bad) return bad;
      }
      return null;
    }
  }
  return null;
};

class Failure extends Error {
  constructor(kind, message) {
    super(message);
    this.kind = kind;
  }
}

const migrate = (doc) => {
  const from = Number.isInteger(doc.schema_version) && doc.schema_version >= 1 ? doc.schema_version : 1;
  if (from >= current) return { id: doc.id, skipped: true };
  const meta = deepFreeze({ ...(doc.meta ?? {}), id: doc.id, version: doc.version });
  let fields = doc.fields ?? {};
  let version = from;
  for (const step of steps) {
    if (step.to <= version) continue;
    if (step.to > current) break;
    if (step.to !== version + 1) {
      throw Object.assign(new Failure("no_step", `no migration from version ${version} to ${version + 1}`), { step: `${version} → ${version + 1}` });
    }
    const label = `${version} → ${step.to}`;
    let result;
    try {
      result = step.run(structuredClone(fields), meta);
    } catch (e) {
      throw Object.assign(new Failure("threw", `${e?.name ?? "Error"}: ${e?.message ?? e}`.slice(0, 300)), { step: label });
    }
    if (result !== null && typeof result === "object" && typeof result.then === "function") {
      result?.catch?.(() => {}); // an async migration's rejection is not an unhandled one
      throw Object.assign(new Failure("async", "a migration must be synchronous: it returned a promise"), { step: label });
    }
    if (result === null || typeof result !== "object" || Array.isArray(result)) {
      throw Object.assign(new Failure("not_json", "a migration must return an object"), { step: label });
    }
    const bad = checkJSON(result, "");
    if (bad) throw Object.assign(new Failure("not_json", `the result doesn't survive JSON: ${bad}`), { step: label });
    fields = JSON.parse(JSON.stringify(result));
    delete fields.id; // backd's own, whatever the migration says
    delete fields._meta;
    version = step.to;
  }
  if (version !== current) {
    throw Object.assign(new Failure("no_step", `no migration from version ${version} to ${current}`), { step: `${version} → ${current}` });
  }
  return { id: doc.id, fields };
};

const results = [];
for (const doc of docs ?? []) {
  try {
    results.push(migrate(doc));
  } catch (e) {
    const kind = e instanceof Failure ? e.kind : "threw";
    results.push({ id: doc.id, error: { kind, step: e?.step ?? "", message: String(e?.message ?? e).slice(0, 300) } });
  }
}
out({ ok: true, output: results });
Deno.exit(0);
