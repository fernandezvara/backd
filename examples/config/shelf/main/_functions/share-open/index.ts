// share-open: the public side of a share link. Given the token it answers
// with a small projection of the asset — never the whole document, never the
// shares row — so a public link exposes exactly what it must, no more.
import { relay } from "../lib/relay.ts";
import type { Context, FileDetails } from "../lib/types.ts";

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

  // Chapter 15: the files of a shared asset come with links that work for
  // whoever holds the share link, until they expire. The share is the
  // permission, so the links are made as the function (ctx.admin.db), after the
  // token and expiry checks above; they carry names and sizes, never ids.
  const files: { name: string; size: number; type: string; url: string; expires_at: string }[] = [];
  for (const field of ["file", "attachments"] as const) {
    const held = asset[field];
    for (const f of (Array.isArray(held) ? held : held ? [held] : []) as FileDetails[]) {
      const link = await db.collection("assets").files(asset.id, field).link(f.id);
      files.push({ name: f.name, size: f.size, type: f.type, url: link.url, expires_at: link.expires_at });
    }
  }
  return { title, kind, url, body, tags, published_at, files };
}
