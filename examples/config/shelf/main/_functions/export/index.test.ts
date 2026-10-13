// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

Deno.test("exports the caller's own assets", async () => {
  const { ctx, store } = createContext({ user: { id: "u1", email: "ana@shelf.example" } });
  store.seed("main", "assets", [
    { id: "a1", title: "Handbook", kind: "link", url: "https://example.com/h", tags: ["docs"], published_at: "2026-10-01T00:00:00Z", _meta: { owner: "u1" } },
    { id: "a2", title: "Someone else's", kind: "note", _meta: { owner: "u2" } },
  ]);
  const out = (await handler(ctx as never)) as unknown as { count: number; assets: { id: string; tags: string[] }[] };
  if (out.count !== 1 || out.assets[0].id !== "a1" || out.assets[0].tags[0] !== "docs") throw new Error(JSON.stringify(out));
});
