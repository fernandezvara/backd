// maintenance-close: hides the banner by returning the whole group to its
// defaults (chapter 18).
import type { Context } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const out = await ctx.admin.config.reset(["maintenance"]);
  return { version: out.version, changed: out.changed };
}
