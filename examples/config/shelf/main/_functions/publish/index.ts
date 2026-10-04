// publish: a curator publishes a member's draft — published_at becomes the
// server's now, a field rules forbid clients to write. `admin: true` writes
// past those rules; ctx.error is how the caller learns why a call failed.
import { relay } from "../lib/relay.ts";
import type { Context, Doc } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { asset_id } = ctx.input as { asset_id: string };
  const db = ctx.admin.db("main");

  let asset: Doc;
  try {
    asset = await db.collection("assets").get(asset_id);
  } catch (err) {
    relay(err, ctx.error);
  }
  if (asset.published_at) {
    throw ctx.error(409, "already_published", `"${asset.title}" is already published`);
  }

  const published_at = new Date().toISOString();
  try {
    await db.collection("assets").patch(asset_id, { published_at });
  } catch (err) {
    relay(err, ctx.error);
  }

  console.log(`published ${asset_id} at ${published_at} (requested by ${ctx.user?.email ?? "api-key"})`);

  // The owner is told — as an afterthought: a failing notification must
  // not undo or hide a publish that already happened.
  if (asset._meta?.owner) {
    try {
      await ctx.call("notify", {
        to_user: asset._meta.owner,
        kind: "asset.published",
        text: `“${asset.title}” was published`,
        asset_id,
      });
    } catch (err) {
      console.log(`the notification was not written: ${err instanceof Error ? err.message : err}`);
    }
  }
  return { asset_id, published_at };
}
