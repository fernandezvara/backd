// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, FunctionError } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const seed = (store: { seed: (db: string, name: string, docs: object[]) => unknown }) => {
  store.seed("main", "assets", [{ id: "a1", title: "Handbook", kind: "link", url: "https://example.com", tags: ["docs"], published_at: "2026-09-12T09:00:00Z" }]);
  store.seed("main", "shares", [{ id: "s1", asset_id: "a1", token: "tok-24-chars-aaaaaaaaaaaa", expires_at: null, created_by: "u1" }]);
};

Deno.test("resolves a valid token to the asset's public projection", async () => {
  const { ctx, store } = createContext({ admin: true, input: { token: "tok-24-chars-aaaaaaaaaaaa" } });
  seed(store);

  const out = (await handler(ctx as never)) as Record<string, unknown>;
  if (out.title !== "Handbook" || out.url !== "https://example.com") throw new Error(JSON.stringify(out));
  // No document ids, no _meta, no shares fields leak through.
  if ("id" in out || "_meta" in out || "asset_id" in out || "token" in out || "created_by" in out) throw new Error(JSON.stringify(out));
});

Deno.test("unknown and expired tokens answer 404 / 410", async () => {
  const { ctx, store } = createContext({ admin: true, input: { token: "nope" } });
  seed(store);
  try {
    await handler(ctx as never);
    throw new Error("should have refused");
  } catch (e) {
    if ((e as FunctionError).status !== 404) throw e;
  }

  store.seed("main", "shares", [{ id: "s2", asset_id: "a1", token: "expired-token-0000000", expires_at: "2000-01-01T00:00:00Z" }]);
  const expired = createContext({ admin: true, store, input: { token: "expired-token-0000000" } });
  try {
    await handler(expired.ctx as never);
    throw new Error("should have refused");
  } catch (e) {
    const err = e as FunctionError;
    if (err.status !== 410 || err.code !== "expired") throw e;
  }
});
