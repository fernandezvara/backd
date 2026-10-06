// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
// The image library stays out: the thumbnailer takes the resize it is given.
import { createContext, FunctionError, MemoryStore } from "../../../../../../clients/functions-testing/src/index.js";
import { makeThumbnailer, WIDTH } from "../lib/thumbnail.ts";

const png = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 1, 2, 3, 4]);

function seeded() {
  const store = new MemoryStore();
  store.seed("main", "assets", [{ id: "a1", title: "Photo", kind: "file" }]);
  store.declareFiles("main", "assets", { attachments: { multiple: true } });
  return store;
}

Deno.test("it stores the shrunken image in the thumbnail field, as the function", async () => {
  const store = seeded();
  const asset = createContext({ store, admin: true }).ctx.admin.db("main").collection("assets");
  await asset.files("a1", "file").put(png, { name: "photo.png", type: "image/png" });

  const seen: number[] = [];
  const run = makeThumbnailer(async (bytes, width) => {
    seen.push(bytes.length, width);
    return bytes.slice(0, 6);
  });
  const { ctx } = createContext({ store, admin: true, user: { id: "u1", email: "ana@shelf.example" }, input: { asset_id: "a1" } });
  const out = (await run(ctx as never)) as { thumbnail: string; size: number };

  if (seen[0] !== png.length || seen[1] !== WIDTH || out.size !== 6) throw new Error(JSON.stringify({ seen, out }));
  const doc = store.all("main", "assets")[0] as { thumbnail: { name: string; type: string; id: string } };
  if (doc.thumbnail.name !== "thumbnail.png" || doc.thumbnail.type !== "image/png" || doc.thumbnail.id !== out.thumbnail) throw new Error(JSON.stringify(doc));
  if (store.fileBytes(out.thumbnail)?.length !== 6) throw new Error("the thumbnail's bytes");
});

Deno.test("a second run replaces the thumbnail", async () => {
  const store = seeded();
  const asset = createContext({ store, admin: true }).ctx.admin.db("main").collection("assets");
  await asset.files("a1", "file").put(png, { type: "image/png" });
  const run = makeThumbnailer(async (b) => b.slice(0, 4));
  const first = (await run(createContext({ store, admin: true, input: { asset_id: "a1" } }).ctx as never)) as { thumbnail: string };
  const second = (await run(createContext({ store, admin: true, input: { asset_id: "a1" } }).ctx as never)) as { thumbnail: string };
  if (first.thumbnail === second.thumbnail || store.fileBytes(first.thumbnail) !== undefined) throw new Error("the old thumbnail stayed");
});

Deno.test("a file that isn't an image is a 422, and an unknown asset a 404", async () => {
  const store = seeded();
  const asset = createContext({ store, admin: true }).ctx.admin.db("main").collection("assets");
  await asset.files("a1", "file").put("hello", { name: "notes.txt", type: "text/plain" });
  const run = makeThumbnailer(async (b) => b);
  const refused = await run(createContext({ store, admin: true, input: { asset_id: "a1" } }).ctx as never).catch((e) => e);
  if (!(refused instanceof Error) || (refused as FunctionError).status !== 422 || (refused as FunctionError).code !== "not_an_image") throw new Error(String(refused));
  const missing = await run(createContext({ store, admin: true, input: { asset_id: "nope" } }).ctx as never).catch((e) => e);
  if ((missing as { status?: number }).status !== 404) throw new Error(String(missing));
});
