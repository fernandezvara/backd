// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, fakeTrigger, MemoryStore } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const published = (depth = 1) =>
  fakeTrigger({
    event: "updated",
    collection: "submissions",
    fields: ["asset_id"],
    depth,
    before: { id: "s1", asset_id: "a1", status: "approved", _meta: { version: 2, owner: "u1" } },
    after: { id: "s1", asset_id: "a1", status: "published", _meta: { version: 3, owner: "u1" } },
  });

Deno.test("thanks the author once; the run knows it is a chained one", async () => {
  const store = new MemoryStore();
  const out = (await handler(createContext({ store, admin: true, input: published(1) }).ctx as never)) as { notified?: string; depth?: number };
  const [note] = store.all("main", "notifications") as { to_user: string; kind: string }[];
  if (!out.notified || out.depth !== 1 || note.to_user !== "u1" || note.kind !== "submission.published") {
    throw new Error(JSON.stringify({ out, note }));
  }
  const again = (await handler(createContext({ store, admin: true, input: published(1) }).ctx as never)) as { skipped?: string };
  if (!again.skipped || store.all("main", "notifications").length !== 1) throw new Error(JSON.stringify(again));
});
