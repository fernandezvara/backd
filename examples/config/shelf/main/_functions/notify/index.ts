// notify: one unread notification for one member. Internal — callers are
// other functions, never HTTP: the notifications collection has no create
// rule either, so this function is the only way a document lands there.
import type { Context, Doc } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { to_user, kind, text, asset_id } = ctx.input as {
    to_user: string; kind: string; text: string; asset_id?: string;
  };

  const note = await ctx.admin.db("main").collection("notifications").create({
    to_user,
    kind,
    text,
    ...(asset_id ? { asset_id } : {}),
    read_at: null,
  });
  return { id: note.id };
}
