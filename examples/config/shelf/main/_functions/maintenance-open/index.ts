// maintenance-open: shows the banner (chapter 18). The input is for runs by hand;
// the schedule runs it without one.
import type { Context } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { message } = (ctx.input ?? {}) as { message?: string };
  const out = await ctx.admin.config.set({
    "maintenance.enabled": true,
    "maintenance.message": message ?? "Maintenance in progress: some things may be slow.",
  });
  return { version: out.version, changed: out.changed };
}
