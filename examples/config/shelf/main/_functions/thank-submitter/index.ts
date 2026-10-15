// thank-submitter: step 3 of the submission pipeline (chapter 23). The proposal
// is published — by publish-submission's own write, so this run is one level
// deeper in the chain than the one that caused it. Tell the author.
import { defineFunction } from "@backd/functions";

export default defineFunction("main/thank-submitter", async (ctx) => {
  const { doc, after } = ctx.input;
  const owner = (after?._meta as { owner?: string } | undefined)?.owner;
  const assetId = after?.asset_id as string | undefined;
  if (!owner || !assetId) return { skipped: "no owner or asset in the input" };

  const notes = ctx.admin.db("main").collection("notifications");
  const known = await notes.list({ where: { to_user: owner, kind: "submission.published", asset_id: assetId }, limit: 1 });
  if (known.items.length > 0) return { skipped: "already thanked" };
  const note = await notes.create({
    to_user: owner,
    kind: "submission.published",
    text: "Your proposal is in the gallery",
    asset_id: assetId,
    read_at: null,
  });
  return { notified: note.id, submission: doc.id, depth: ctx.input.depth };
});
