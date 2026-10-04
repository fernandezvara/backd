// share-open: the public side of a share link. Given the token it answers
// with a small projection of the asset — never the whole document, never the
// shares row — so a public link exposes exactly what it must, no more.
import { relay } from "../lib/relay.ts";
import type { Context } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { token } = ctx.input as { token: string };
  const db = ctx.admin.db("main");

  const page = await db.collection("shares").list({ where: { token }, limit: 1 });
  const share = page.items[0];
  if (!share) {
    throw ctx.error(404, "not_found", "This link doesn't exist or was revoked");
  }
  if (share.expires_at && new Date(share.expires_at as string).getTime() < Date.now()) {
    throw ctx.error(410, "expired", "This link has expired");
  }

  let asset;
  try {
    asset = await db.collection("assets").get(share.asset_id as string);
  } catch (err) {
    relay(err, ctx.error);
  }
  const { title, kind, url, body, tags, published_at } = asset;
  return { title, kind, url, body, tags, published_at };
}
