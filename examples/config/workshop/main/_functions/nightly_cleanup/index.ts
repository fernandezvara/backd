// nightly_cleanup: delete draft orders nobody finished within 30 days.
//
// It only ever deletes drafts that are already past the cutoff, so running
// it twice (a lost worker's job is picked up again) is harmless.
import type { Context } from "../lib/types.ts";

const DAYS = 30;

export default async function handler(ctx: Context) {
  const orders = ctx.admin.db("main").collection("orders");
  const cutoff = new Date(Date.now() - DAYS * 86_400_000).toISOString();

  let deleted = 0;
  for (;;) {
    const page = await orders.list({ where: { status: "draft", "_meta.created_at": { $lt: cutoff } }, limit: 100 });
    if (page.items.length === 0) break;
    await ctx.admin.db("main").batch(page.items.map((o) => ({ op: "delete" as const, collection: "orders", id: o.id })));
    deleted += page.items.length;
  }
  console.log(`deleted ${deleted} draft orders older than ${DAYS} days`);
  return { deleted, cutoff };
}
