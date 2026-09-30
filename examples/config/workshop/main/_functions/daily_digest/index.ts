// daily_digest: one `digests` document per UTC day with that day's order
// count and paid revenue. A scheduled run summarizes yesterday; a call made
// by hand can name any day ({"day": "2026-09-28"}). The day is the key (a
// unique index backs it), so running it again for the same day writes
// nothing new.
//
// To send the digest by email instead, keep the aggregation and replace the
// last create with a fetch to your mail provider's API (declare its host
// under `network:` and its key under `secrets:`; see the docs, Functions ->
// Secrets and Outbound network).
import type { Context } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const db = ctx.admin.db("main");

  // A scheduled run has no input: default to yesterday, in UTC.
  const requested = (ctx.input as { day?: string } | null)?.day;
  const today = new Date();
  today.setUTCHours(0, 0, 0, 0);
  const day = requested ?? new Date(today.getTime() - 86_400_000).toISOString().slice(0, 10);
  const start = new Date(`${day}T00:00:00Z`);
  const end = new Date(start.getTime() + 86_400_000);

  const digests = db.collection("digests");
  if ((await digests.list({ where: { day }, limit: 1 })).items.length > 0) {
    return { day, skipped: true };
  }

  let orders = 0;
  let revenue = 0;
  const created = { "_meta.created_at": { $gte: start.toISOString(), $lt: end.toISOString() } };
  for await (const order of db.collection("orders").iterate({ where: created })) {
    orders++;
    if (order.status === "paid") revenue += order.amount as number;
  }

  const digest = await digests.create({ day, orders, revenue });
  return { day, orders, revenue, digest_id: digest.id, skipped: false };
}
