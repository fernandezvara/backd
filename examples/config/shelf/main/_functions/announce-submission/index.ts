// announce-submission: step 1 of the submission pipeline (chapter 23). A member
// proposed a draft; say that it is waiting. The notification is written by a
// trigger run, so backd stamps it with where it sits in the chain.
import { defineFunction } from "@backd/functions";

export default defineFunction("main/announce-submission", async (ctx) => {
  const { doc, after } = ctx.input;
  const owner = (after?._meta as { owner?: string } | undefined)?.owner;
  const assetId = after?.asset_id as string | undefined;
  if (!owner || !assetId) return { skipped: "no owner or asset in the input" };

  const notes = ctx.admin.db("main").collection("notifications");
  // Idempotent: the same change delivered twice must not notify twice.
  const known = await notes.list({ where: { to_user: owner, kind: "submission.received", asset_id: assetId }, limit: 1 });
  if (known.items.length > 0) return { skipped: "already announced" };
  const note = await notes.create({
    to_user: owner,
    kind: "submission.received",
    text: "Your proposal is waiting for a curator",
    asset_id: assetId,
    read_at: null,
  });
  return { notified: note.id, submission: doc.id };
});
