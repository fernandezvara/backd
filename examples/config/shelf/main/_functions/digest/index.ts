// digest: one notification per member listing what was published recently.
// Async — the caller gets a job, not the answer; retry covers a transient
// failure mid-run. The member list comes from the members collection: the
// realm's users live in the system database, which functions can't read.
import type { Context, Doc } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const sinceDays = (ctx.input as { since_days?: number } | null)?.since_days ?? 7;
  const db = ctx.admin.db("main");
  const since = new Date(Date.now() - sinceDays * 86_400_000).toISOString();

  const page = await db.collection("assets").list({
    where: { published_at: { $gte: since } },
    orderBy: "published_at",
    limit: 100,
  });
  if (page.items.length === 0) {
    return { since_days: sinceDays, notified: 0, skipped: true };
  }

  const titles = page.items.map((a) => `“${a.title}”`).join(", ");
  const text = `${page.items.length} published since ${since.slice(0, 10)}: ${titles}`;

  let notified = 0;
  for await (const member of db.collection("members").iterate()) {
    await ctx.call("notify", {
      to_user: member.user_id,
      kind: "digest",
      text,
    });
    notified++;
  }
  console.log(`digest: ${page.items.length} assets, ${notified} members notified`);
  return { since_days: sinceDays, assets: page.items.length, notified, skipped: false };
}
