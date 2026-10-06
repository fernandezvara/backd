// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, MemoryStore } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

async function seeded() {
  const store = new MemoryStore();
  store.seed("main", "assets", [{ id: "a1", title: "Handbook", kind: "file" }]);
  store.declareFiles("main", "assets", { attachments: { multiple: true } });
  const admin = createContext({ store, admin: true }).ctx.admin.db("main").collection("assets");
  const doc = await admin.files("a1", "file").put("the handbook", { name: "handbook.txt", type: "text/plain" });
  return { store, fileId: (doc.file as { id: string }).id };
}

Deno.test("it returns the link and counts the download", async () => {
  const { store, fileId } = await seeded();
  const call = () => handler(createContext({ store, admin: true, user: { id: "u1", email: "ana@shelf.example" }, input: { document_id: "a1", field: "file", file_id: fileId } }).ctx as never);
  const out = (await call()) as { url: string; expires_at: string };
  if (!out.url.startsWith("https://") || !out.expires_at) throw new Error(JSON.stringify(out));
  await call();
  const doc = store.all("main", "assets")[0] as { downloads: number };
  if (doc.downloads !== 2) throw new Error(`downloads: ${doc.downloads}`);
});

Deno.test("a file that isn't there is a 404 and counts nothing", async () => {
  const { store } = await seeded();
  const err = await handler(createContext({ store, admin: true, input: { document_id: "a1", field: "file", file_id: "fl_nope" } }).ctx as never).catch((e) => e);
  if ((err as { status?: number }).status !== 404) throw new Error(String(err));
  const unknown = await handler(createContext({ store, admin: true, input: { document_id: "zzz", field: "file" } }).ctx as never).catch((e) => e);
  if ((unknown as { status?: number }).status !== 404) throw new Error(String(unknown));
  if ((store.all("main", "assets")[0] as { downloads?: number }).downloads !== undefined) throw new Error("counted a failed download");
});
