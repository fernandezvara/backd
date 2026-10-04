// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

Deno.test("writes an unread notification for the addressee", async () => {
  const { ctx, store } = createContext({
    admin: true,
    input: { to_user: "u1", kind: "asset.published", text: "“Handbook” was published", asset_id: "a1" },
  });

  const out = (await handler(ctx as never)) as { id: string };
  const [note] = store.all("main", "notifications");
  if (!out.id || note.to_user !== "u1" || note.kind !== "asset.published" || note.read_at !== null) {
    throw new Error(JSON.stringify(note));
  }
});
