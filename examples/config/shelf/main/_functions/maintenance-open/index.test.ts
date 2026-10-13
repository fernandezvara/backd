// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

Deno.test("opens the window with the message given, or a default", async () => {
  const { ctx, configChanges } = createContext({
    config: { maintenance: { enabled: false, message: "" } }, configWrite: true, input: { message: "Back at 04:00" },
  });
  const out = (await handler(ctx as never)) as { changed: boolean };
  if (!out.changed) throw new Error("nothing changed");
  const changes = configChanges();
  if (!changes.some((c: { path: string; new: unknown }) => c.path === "maintenance.enabled" && c.new === true) ||
      !changes.some((c: { path: string; new: unknown }) => c.path === "maintenance.message" && c.new === "Back at 04:00")) {
    throw new Error(JSON.stringify(changes));
  }
});
