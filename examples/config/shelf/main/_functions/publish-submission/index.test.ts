// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, fakeTrigger, MemoryStore } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const approved = (by?: string) =>
  fakeTrigger({
    event: "updated",
    collection: "submissions",
    fields: ["asset_id"],
    by,
    before: { id: "s1", asset_id: "a1", status: "submitted", _meta: { version: 1 } },
    after: { id: "s1", asset_id: "a1", status: "approved", _meta: { version: 2 } },
  });

function seeded(status = "approved") {
  const store = new MemoryStore();
  store.seed("main", "submissions", [{ id: "s1", asset_id: "a1", status, decided_by: null }]);
  store.seed("main", "assets", [{ id: "a1", title: "Handbook", kind: "note", published_at: null }]);
  return store;
}

Deno.test("publishes the asset and marks the proposal, naming the curator", async () => {
  const store = seeded();
  const out = (await handler(createContext({ store, admin: true, input: approved("cur1") }).ctx as never)) as { published_by: string };
  const [asset] = store.all("main", "assets") as { published_at: string | null }[];
  const [sub] = store.all("main", "submissions") as { status: string; decided_by: string | null }[];
  if (out.published_by !== "cur1" || !asset.published_at || sub.status !== "published" || sub.decided_by !== "cur1") {
    throw new Error(JSON.stringify({ out, asset, sub }));
  }
});

Deno.test("running it again does nothing more: the proposal already moved on", async () => {
  const store = seeded();
  await handler(createContext({ store, admin: true, input: approved("cur1") }).ctx as never);
  const [first] = store.all("main", "assets") as { published_at: string }[];
  const again = (await handler(createContext({ store, admin: true, input: approved("cur1") }).ctx as never)) as { skipped?: string };
  const [second] = store.all("main", "assets") as { published_at: string }[];
  if (!again.skipped || first.published_at !== second.published_at) throw new Error(JSON.stringify({ again, first, second }));
});

Deno.test("a delivery that arrives while the proposal is still submitted is left alone", async () => {
  const store = seeded("submitted");
  const out = (await handler(createContext({ store, admin: true, input: approved("cur1") }).ctx as never)) as { skipped?: string };
  const [asset] = store.all("main", "assets") as { published_at: string | null }[];
  if (!out.skipped || asset.published_at !== null) throw new Error(JSON.stringify({ out, asset }));
});

Deno.test("without a user behind the approval there is no decided_by (null in a merge patch removes the field)", async () => {
  const store = seeded();
  await handler(createContext({ store, admin: true, input: approved(undefined) }).ctx as never);
  const [sub] = store.all("main", "submissions") as { decided_by?: string | null }[];
  if (sub.decided_by != null) throw new Error(JSON.stringify(sub));
});
