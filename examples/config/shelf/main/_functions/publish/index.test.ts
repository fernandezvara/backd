// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, FunctionError } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const curator = { id: "u2", email: "curator@shelf.example", roles: ["curator"] };

Deno.test("stamps published_at on a draft and notifies the owner", async () => {
  const { ctx, store, fakeCall, calls } = createContext({ admin: true, user: curator, input: { asset_id: "a1" } });
  store.seed("main", "assets", [{ id: "a1", title: "A draft", kind: "note", _meta: { owner: "u1" } }]);
  fakeCall("notify", () => ({ id: "n1" }));

  const out = (await handler(ctx as never)) as { asset_id: string; published_at: string };
  if (out.asset_id !== "a1" || !out.published_at) throw new Error(JSON.stringify(out));
  const [saved] = store.all("main", "assets");
  if (saved.published_at !== out.published_at) throw new Error(`stored ${saved.published_at}, returned ${out.published_at}`);
  const [call] = calls("notify");
  if (call.input.to_user !== "u1" || call.input.kind !== "asset.published" || call.input.asset_id !== "a1") {
    throw new Error(JSON.stringify(call));
  }
});

Deno.test("refuses an asset that is already published", async () => {
  const { ctx, store } = createContext({ admin: true, user: curator, input: { asset_id: "a1" } });
  store.seed("main", "assets", [{ id: "a1", title: "Old news", kind: "note", published_at: "2026-09-12T09:00:00Z" }]);

  try {
    await handler(ctx as never);
    throw new Error("should have refused");
  } catch (e) {
    const err = e as FunctionError;
    if (err.status !== 409 || err.code !== "already_published") throw e;
  }
});

Deno.test("relays a missing asset as 404", async () => {
  const { ctx } = createContext({ admin: true, user: curator, input: { asset_id: "nope" } });

  try {
    await handler(ctx as never);
    throw new Error("should have refused");
  } catch (e) {
    const err = e as FunctionError;
    if (err.status !== 404) throw e;
  }
});
