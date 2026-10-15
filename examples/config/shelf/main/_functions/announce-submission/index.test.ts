// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, fakeTrigger, MemoryStore } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const created = () =>
  fakeTrigger({
    event: "created",
    collection: "submissions",
    fields: ["asset_id"],
    after: { id: "s1", asset_id: "a1", status: "submitted", _meta: { version: 1, owner: "u1" } },
  });

Deno.test("tells the member the proposal waits, and only once", async () => {
  const store = new MemoryStore();
  const first = (await handler(createContext({ store, admin: true, input: created() }).ctx as never)) as { notified?: string };
  const [note] = store.all("main", "notifications") as { to_user: string; kind: string; asset_id: string; read_at: unknown }[];
  if (!first.notified || note.to_user !== "u1" || note.kind !== "submission.received" || note.asset_id !== "a1" || note.read_at !== null) {
    throw new Error(JSON.stringify({ first, note }));
  }
  // The same change delivered again (a restart, a handoff) notifies nobody twice.
  const again = (await handler(createContext({ store, admin: true, input: created() }).ctx as never)) as { skipped?: string };
  if (!again.skipped || store.all("main", "notifications").length !== 1) throw new Error(JSON.stringify(again));
});

Deno.test("an input without the asset (no `fields`) is skipped, not an error", async () => {
  const store = new MemoryStore();
  const bare = fakeTrigger({ event: "created", collection: "submissions", after: { id: "s2", _meta: { version: 1 } } });
  const out = (await handler(createContext({ store, admin: true, input: bare }).ctx as never)) as { skipped?: string };
  if (!out.skipped || store.all("main", "notifications").length !== 0) throw new Error(JSON.stringify(out));
});
