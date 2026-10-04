// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

Deno.test("removes expired shares and old read notifications, keeps the rest", async () => {
  const { ctx, store } = createContext({ admin: true, input: null });
  const past = "2001-01-01T00:00:00Z";
  const future = "2999-01-01T00:00:00Z";
  store.seed("main", "shares", [
    { id: "s-old", asset_id: "a1", token: "aaaaaaaaaaaaaaaaaaaaaaaa", expires_at: past },
    { id: "s-live", asset_id: "a1", token: "bbbbbbbbbbbbbbbbbbbbbbbb", expires_at: future },
    { id: "s-forever", asset_id: "a1", token: "cccccccccccccccccccccccc", expires_at: null },
  ]);
  store.seed("main", "notifications", [
    { id: "n-read-old", to_user: "u1", kind: "digest", text: "x", read_at: past, _meta: { created_at: past, updated_at: past } },
    { id: "n-read-new", to_user: "u1", kind: "digest", text: "x", read_at: new Date().toISOString() },
    { id: "n-unread-old", to_user: "u1", kind: "digest", text: "x", read_at: null, _meta: { created_at: past, updated_at: past } },
  ]);

  const out = (await handler(ctx as never)) as { shares: number; notifications: number };
  if (out.shares !== 1 || out.notifications !== 1) throw new Error(JSON.stringify(out));

  const shares = store.all("main", "shares").map((s) => s.id).sort();
  const notifs = store.all("main", "notifications").map((n) => n.id).sort();
  if (shares.join() !== "s-forever,s-live") throw new Error(shares.join());
  if (notifs.join() !== "n-read-new,n-unread-old") throw new Error(notifs.join());
});
