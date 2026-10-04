// import: handles "asset pushed" events from a feed. The feed signs the raw
// body (x-signature: sha256=<hex>) and retries any delivery it didn't get a
// 2xx for, so the same event can arrive more than once.
//
// Imported assets arrive as drafts — published_at is the publish function's
// alone, even for a trusted sender.
import { verifySignature } from "../lib/signature.ts";
import type { Context } from "../lib/types.ts";

type ImportEvent = { event_id: string; title: string; kind?: string; url?: string; body?: string; tags?: string[] };

export default async function handler(ctx: Context) {
  // 1. Verify the sender, against the exact bytes it signed.
  const { body, headers } = ctx.request;
  if (!(await verifySignature(ctx.secrets.IMPORT_WEBHOOK_SECRET, body, headers["x-signature"]))) {
    return { status: 400, body: "invalid signature" };
  }

  // 2. Shape the event; the body is untrusted JSON, so fields are whitelisted.
  const event = JSON.parse(body) as ImportEvent;
  if (!event.event_id || !event.title) {
    return { status: 422, body: "need event_id and title" };
  }

  // 3. Deduplicate by the feed's own event id: record the event FIRST. The
  //    unique index on imports.event_id refuses a second insert with a 409,
  //    so a retried delivery stops here — before it can make a second asset.
  const db = ctx.admin.db("main");
  let record;
  try {
    record = await db.collection("imports").create({ event_id: event.event_id });
  } catch (err) {
    if ((err as { status?: number }).status === 409) return { status: 200, body: "already processed" };
    throw err;
  }

  const doc: Record<string, unknown> = { title: event.title, kind: event.kind === "note" ? "note" : "link" };
  if (event.url) doc.url = event.url;
  if (event.body) doc.body = event.body;
  doc.tags = [...(event.tags ?? []), "imported"];
  const asset = await db.collection("assets").create(doc);
  await db.collection("imports").patch(record.id, { asset_id: asset.id });

  return { status: 200, body: "ok" };
}
