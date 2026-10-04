// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, FunctionError } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const member = { id: "u1", email: "ana@shelf.example", roles: [] };

Deno.test("mints a server-side link for a published asset", async () => {
  const { ctx, store } = createContext({ admin: true, user: member, input: { asset_id: "a1", expires_in: "week" } });
  store.seed("main", "assets", [{ id: "a1", title: "Handbook", kind: "link", published_at: "2026-09-12T09:00:00Z" }]);

  const out = (await handler(ctx as never)) as { token: string; expires_at: string };
  if (typeof out.token !== "string" || out.token.length !== 24) throw new Error(`token ${out.token}`);
  const [share] = store.all("main", "shares");
  if (share.asset_id !== "a1" || share.token !== out.token || share.created_by !== "u1") throw new Error(JSON.stringify(share));
  if (new Date(share.expires_at) <= new Date()) throw new Error("expires_at should be in the future");
});

Deno.test("refuses to share a draft", async () => {
  const { ctx, store } = createContext({ admin: true, user: member, input: { asset_id: "a1" } });
  store.seed("main", "assets", [{ id: "a1", title: "Draft", kind: "note" }]);

  try {
    await handler(ctx as never);
    throw new Error("should have refused");
  } catch (e) {
    const err = e as FunctionError;
    if (err.status !== 409 || err.code !== "not_published") throw e;
  }
});

Deno.test("expires_in: never makes a link without an expiry", async () => {
  const { ctx, store } = createContext({ admin: true, user: member, input: { asset_id: "a1", expires_in: "never" } });
  store.seed("main", "assets", [{ id: "a1", title: "Handbook", kind: "link", published_at: "2026-09-12T09:00:00Z" }]);

  const out = (await handler(ctx as never)) as { expires_at: string | null };
  if (out.expires_at !== null) throw new Error(JSON.stringify(out));
});
