// download: counts a download and returns the link the app opens (chapter 15).
// A function is called with POST and answers JSON, so it can't stream the file:
// it hands over the link. Two identities do two jobs:
//   - ctx.db, the caller: asking for the link is reading the asset, so the
//     asset's `read` rule decides who gets one (an asset they can't read is a 404);
//   - ctx.admin.db, the function: the count is its own, in a field the rules
//     refuse to clients.
import { relay } from "../lib/relay.ts";
import type { Context } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { document_id, field, file_id } = ctx.input as { document_id: string; field: "file" | "attachments"; file_id?: string };

  let link;
  try {
    link = await ctx.db("main").collection("assets").files(document_id, field).link(file_id);
  } catch (err) {
    relay(err, ctx.error);
  }

  // Increment, conditional on the version read; a clash with another download retries.
  const assets = ctx.admin.db("main").collection("assets");
  for (let attempt = 0; attempt < 5; attempt++) {
    const asset = await assets.get(document_id);
    try {
      await assets.patch(document_id, { downloads: ((asset.downloads as number | undefined) ?? 0) + 1 }, { ifMatch: asset._meta.version });
      break;
    } catch (err) {
      if ((err as { code?: string }).code !== "version_mismatch" || attempt === 4) relay(err, ctx.error);
    }
  }
  return { url: link!.url, expires_at: link!.expires_at };
}
