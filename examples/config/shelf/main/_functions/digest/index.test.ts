// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

Deno.test("notifies every member about assets published in the window", async () => {
  const { ctx, store, fakeCall, calls } = createContext({ admin: true, input: { since_days: 7 } });
  store.seed("main", "assets", [
    { id: "a1", title: "Handbook", kind: "link", published_at: new Date().toISOString() },
    { id: "a2", title: "Old guide", kind: "link", published_at: "2001-01-01T00:00:00Z" },
    { id: "a3", title: "Draft", kind: "note" },
  ]);
  store.seed("main", "members", [
    { user_id: "u1", email: "ana@shelf.example" },
    { user_id: "u2", email: "ben@shelf.example" },
  ]);
  fakeCall("notify", () => ({ id: "n" }));

  const out = (await handler(ctx as never)) as { assets: number; notified: number };
  if (out.assets !== 1 || out.notified !== 2) throw new Error(JSON.stringify(out));
  const sent = calls("notify").map((c) => c.input);
  if (sent.length !== 2 || !sent.every((n) => n.kind === "digest") || new Set(sent.map((n) => n.to_user)).size !== 2) {
    throw new Error(JSON.stringify(sent));
  }
  if (!sent[0].text.includes("Handbook") || sent[0].text.includes("Old guide") || sent[0].text.includes("Draft")) {
    throw new Error(JSON.stringify(sent[0]));
  }
});

Deno.test("a quiet period notifies nobody", async () => {
  const { ctx, store, calls } = createContext({ admin: true, input: { since_days: 7 } });
  store.seed("main", "members", [{ user_id: "u1", email: "ana@shelf.example" }]);

  const out = (await handler(ctx as never)) as { notified: number; skipped: boolean };
  if (out.notified !== 0 || !out.skipped || calls("notify").length !== 0) throw new Error(JSON.stringify(out));
});
