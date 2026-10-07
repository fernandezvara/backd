// The thumbnail logic, apart from the image library: given a way to shrink
// image bytes, it reads the asset's picture and stores the result in the
// `thumbnail` field, which the rules keep from clients. Keeping the library
// out of here makes the function testable without it.
import type { Context } from "./types.ts";

export type Resize = (bytes: Uint8Array, width: number) => Promise<Uint8Array>;

export const WIDTH = 256;

export function makeThumbnailer(resize: Resize) {
  return async (ctx: Context) => {
    const { asset_id, field = "file", file_id } = ctx.input as { asset_id: string; field?: string; file_id?: string };

    // The caller (who queued this job) must be able to read the asset: that is
    // the permission to make a thumbnail of it. Writing the thumbnail is the
    // function's own business, so it goes through ctx.admin.db.
    const asset = await ctx.db("main").collection("assets").get(asset_id);
    const source = await ctx.admin.db("main").collection("assets").files(asset.id, field).get(file_id);
    const type = source.file?.type ?? "";
    if (!type.startsWith("image/")) {
      throw ctx.error(422, "not_an_image", `${source.file?.name ?? "that file"} is ${type || "not an image"}: only images get a thumbnail`);
    }

    ctx.step("resize", { total: 2 });
    const small = await resize(await source.bytes(), WIDTH);
    ctx.progress(1, "resized");

    // A single field: the new thumbnail replaces the old one, whose object backd deletes.
    const doc = await ctx.admin.db("main").collection("assets").files(asset.id, "thumbnail")
      .put(small, { name: "thumbnail.png", type: "image/png" });
    ctx.progress(2, "stored");
    return { thumbnail: (doc.thumbnail as { id: string }).id, size: small.length };
  };
}
