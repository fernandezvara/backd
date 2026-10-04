// share: a member creates a public link to a published asset. The token is
// minted here — 24 random characters, not the client's word — and the asset
// must exist and be published. Chapter 9's cleanup deletes expired links.
import { relay } from "../lib/relay.ts";
import type { Context, Doc } from "../lib/types.ts";

const DAYS: Record<string, number> = { week: 7, month: 30, never: 0 };

export default async function handler(ctx: Context) {
  const { asset_id, expires_in = "week" } = ctx.input as { asset_id: string; expires_in?: keyof typeof DAYS };
  const db = ctx.admin.db("main");

  let asset: Doc;
  try {
    asset = await db.collection("assets").get(asset_id);
  } catch (err) {
    relay(err, ctx.error);
  }
  if (!asset.published_at) {
    throw ctx.error(409, "not_published", "Only a published asset can be shared");
  }

  const days = DAYS[expires_in];
  const share = await db.collection("shares").create({
    asset_id,
    token: crypto.randomUUID().replaceAll("-", "").slice(0, 24),
    expires_at: days ? new Date(Date.now() + days * 86400_000).toISOString() : null,
    created_by: ctx.user?.id ?? null,
  });
  return { token: share.token, expires_at: share.expires_at };
}
