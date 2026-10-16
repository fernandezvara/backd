// publish-submission: step 2 of the submission pipeline (chapter 23). A curator
// moved a proposal from submitted to approved; `by` is that curator. Publish the
// asset and mark the proposal published, which fires step 3.
//
// The trigger can deliver the same change again after a restart, so the function
// looks at where the proposal stands now and does nothing that was already done.
import { defineFunction } from "@backd/functions";
import type { DateTimeString } from "../backd.d.ts";

export default defineFunction("main/publish-submission", async (ctx) => {
  const { doc, by } = ctx.input;
  const main = ctx.admin.db("main");
  const submissions = main.collection("submissions");

  const submission = await submissions.get(doc.id);
  if (submission.status !== "approved") return { skipped: `it is ${submission.status}` };

  const assets = main.collection("assets");
  const asset = await assets.get(submission.asset_id as string);
  if (!asset.published_at) {
    await assets.patch(asset.id, { published_at: new Date().toISOString() as DateTimeString }, { ifMatch: asset._meta.version });
  }
  // decided_by is the curator who approved: `by`, when a user did the approving.
  await submissions.patch(doc.id, { status: "published", decided_by: by ?? null }, { ifMatch: submission._meta.version });
  return { asset: asset.id, published_by: by ?? null };
});
