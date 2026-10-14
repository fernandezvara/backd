// maintenance-close: hides the banner by returning the whole group to its
// defaults (chapter 18).
import { defineFunction } from "@backd/functions";

export default defineFunction("main/maintenance-close", async (ctx) => {
  const out = await ctx.admin.config.reset(["maintenance"]);
  return { version: out.version, changed: out.changed };
});
