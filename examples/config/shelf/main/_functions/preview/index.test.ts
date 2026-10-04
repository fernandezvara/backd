// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
//
// `network:` is enforced at the egress, outside the function — a unit test
// can't see it. What the test can do is replace globalThis.fetch and check
// the code that surrounds the call.
import { createContext, FunctionError } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

async function withFetch(fake: (url: string) => Promise<Response>, run: () => Promise<void>) {
  const real = globalThis.fetch;
  globalThis.fetch = fake as typeof fetch;
  try {
    await run();
  } finally {
    globalThis.fetch = real;
  }
}

Deno.test("returns title and description, sending the url to the allowlisted host", async () => {
  let asked = "";
  await withFetch(async (url) => {
    asked = url;
    return new Response(JSON.stringify({ status: "success", data: { title: "backd docs", description: "the manual" } }), { status: 200 });
  }, async () => {
    const { ctx } = createContext({ input: { url: "https://backd.example/docs" } });
    const out = (await handler(ctx as never)) as { title: string | null; description: string | null };
    if (out.title !== "backd docs" || out.description !== "the manual") throw new Error(JSON.stringify(out));
  });
  if (!asked.startsWith("https://api.microlink.io?url=") || !asked.includes(encodeURIComponent("https://backd.example/docs"))) {
    throw new Error(`fetched ${asked}`);
  }
});

Deno.test("a failed fetch becomes 424 preview_failed (ctx.error is 4xx only)", async () => {
  await withFetch(async () => new Response("down", { status: 503 }), async () => {
    const { ctx } = createContext({ input: { url: "https://anything.example" } });
    try {
      await handler(ctx as never);
      throw new Error("should have refused");
    } catch (e) {
      const err = e as FunctionError;
      if (err.status !== 424 || err.code !== "preview_failed") throw e;
    }
  });
});
