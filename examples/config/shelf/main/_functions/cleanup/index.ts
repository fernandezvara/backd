// cleanup: sweep what time invalidates — expired share links, and
// notifications already read and a month old. A scheduled run has no
// caller and no input; ctx.admin.db is always there.
import type { Context } from "../lib/types.ts";

const KEEP_READ_NOTIFICATIONS = 30 * 86_400_000; // 30 days

export default async function handler(ctx: Context) {
  const db = ctx.admin.db("main");
  const now = new Date().toISOString();
  const staleBefore = new Date(Date.now() - KEEP_READ_NOTIFICATIONS).toISOString();

  let shares = 0;
  // $ne: null because a null expires_at (a link that never dies) must not
  // match "expires before now".
  for await (const s of db.collection("shares").iterate({ where: { expires_at: { $ne: null, $lt: now } } })) {
    await db.collection("shares").delete(s.id);
    shares++;
  }

  let notifications = 0;
  for await (
    const n of db.collection("notifications").iterate({
      where: { read_at: { $ne: null }, "_meta.created_at": { $lt: staleBefore } },
    })
  ) {
    await db.collection("notifications").delete(n.id);
    notifications++;
  }

  console.log(`cleanup: removed ${shares} expired shares, ${notifications} old notifications`);
  return { shares, notifications };
}
