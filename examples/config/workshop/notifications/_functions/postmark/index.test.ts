// Run with `make functions-testing-test` (Deno; no network: fetch is replaced).
import { createContext, emailMessage, FunctionError } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const secrets = { POSTMARK_TOKEN: "server-token", POSTMARK_FROM: "Acme <no-reply@acme.example>" };

type Call = { url: string; init: RequestInit };

/** Replaces fetch for one test: records the calls, answers with `answer`. */
async function withFetch<T>(answer: () => Response | Promise<Response>, test: (calls: Call[]) => Promise<T>): Promise<T> {
  const calls: Call[] = [];
  const real = globalThis.fetch;
  globalThis.fetch = ((url: string, init: RequestInit) => {
    calls.push({ url, init });
    return Promise.resolve(answer());
  }) as typeof fetch;
  try {
    return await test(calls);
  } finally {
    globalThis.fetch = real;
  }
}

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

Deno.test("sends the message through Postmark's API and returns its MessageID", async () => {
  const message = emailMessage({ kind: "reset-password", subject: "Reset your password", reply_to: "help@acme.example", text: "Open the link", html: "<p>Open the link</p>" });
  const { ctx } = createContext({ input: message, secrets });
  await withFetch(() => json(200, { ErrorCode: 0, Message: "OK", MessageID: "pm-1" }), async (calls) => {
    const out = await handler(ctx as never) as { message_id: string };
    if (out.message_id !== "pm-1") throw new Error(JSON.stringify(out));
    if (calls.length !== 1 || calls[0].url !== "https://api.postmarkapp.com/email") throw new Error(JSON.stringify(calls));
    const headers = calls[0].init.headers as Record<string, string>;
    if (headers["X-Postmark-Server-Token"] !== "server-token") throw new Error("the token must come from the secret");
    const body = JSON.parse(calls[0].init.body as string);
    const want = {
      From: "Acme <no-reply@acme.example>", To: "ana@example.com", ReplyTo: "help@acme.example", Subject: "Reset your password",
      TextBody: "Open the link", HtmlBody: "<p>Open the link</p>", Tag: "reset-password", Metadata: { backd_email_id: "email-test" }, MessageStream: "outbound",
    };
    if (JSON.stringify(body) !== JSON.stringify(want)) throw new Error(JSON.stringify(body));
  });
});

Deno.test("several recipients, names, cc and bcc are joined the way Postmark wants", async () => {
  const message = emailMessage({
    to: [{ email: "ana@example.com", name: "Ana" }, { email: "bob@example.com", name: null }],
    cc: [{ email: "cc@example.com", name: null }],
    bcc: [{ email: "bcc@example.com", name: null }],
  });
  const { ctx } = createContext({ input: message, secrets });
  await withFetch(() => json(200, { ErrorCode: 0, MessageID: "pm-2" }), async (calls) => {
    await handler(ctx as never);
    const body = JSON.parse(calls[0].init.body as string);
    if (body.To !== "Ana <ana@example.com>,bob@example.com" || body.Cc !== "cc@example.com" || body.Bcc !== "bcc@example.com") throw new Error(JSON.stringify(body));
  });
});

Deno.test("a message Postmark refuses on its merits is a permanent 4xx error", async () => {
  const { ctx } = createContext({ input: emailMessage(), secrets });
  await withFetch(() => json(422, { ErrorCode: 406, Message: "You tried to send to a recipient that has been marked as inactive." }), async () => {
    try {
      await handler(ctx as never);
    } catch (err) {
      const e = err as FunctionError;
      if (!(e instanceof FunctionError) || e.status !== 422 || e.code !== "postmark_406" || !e.message.includes("inactive")) throw err;
      return;
    }
    throw new Error("it should have thrown");
  });
});

Deno.test("anything else is a plain error, so backd retries it", async () => {
  const { ctx } = createContext({ input: emailMessage(), secrets });
  for (const status of [401, 429, 500, 503]) {
    await withFetch(() => json(status, { Message: "nope" }), async () => {
      try {
        await handler(ctx as never);
      } catch (err) {
        if (err instanceof FunctionError || !(err instanceof Error) || !err.message.includes(String(status))) throw err;
        return;
      }
      throw new Error(`${status} should have thrown`);
    });
  }
  // The network failing is retried too, and a non-JSON body doesn't hide the status.
  await withFetch(() => Promise.reject(new TypeError("network down")), async () => {
    try {
      await handler(ctx as never);
    } catch (err) {
      if (err instanceof FunctionError) throw err;
      return;
    }
    throw new Error("a network failure should have thrown");
  });
  await withFetch(() => new Response("<html>bad gateway</html>", { status: 502 }), async () => {
    try {
      await handler(ctx as never);
    } catch (err) {
      if (err instanceof FunctionError || !(err as Error).message.includes("502")) throw err;
      return;
    }
    throw new Error("502 should have thrown");
  });
});
