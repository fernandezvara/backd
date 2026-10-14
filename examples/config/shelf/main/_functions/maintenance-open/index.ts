// maintenance-open: shows the banner (chapter 18). The input is for runs by hand;
// the schedule runs it without one.
import { defineFunction } from "@backd/functions";

export default defineFunction("main/maintenance-open", async (ctx) => {
  const { message } = (ctx.input ?? {}) as { message?: string };
  const out = await ctx.admin.config.set({
    "maintenance.enabled": true,
    "maintenance.message": message ?? "Maintenance in progress: some things may be slow.",
  });
  return { version: out.version, changed: out.changed };
});
