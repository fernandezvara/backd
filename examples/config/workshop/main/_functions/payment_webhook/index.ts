// payment_webhook: handles "payment.succeeded" events from a payment
// provider. The provider signs the raw body (x-signature: sha256=<hex>) and
// retries any delivery it didn't get a 2xx for, so the same event can
// arrive more than once.
import { verifySignature } from "../lib/signature.ts";
import type { Context } from "../lib/types.ts";

type PaymentEvent = { id: string; type: string; order_id: string };

export default async function handler(ctx: Context) {
  // 1. Verify the sender, against the exact bytes it signed.
  const { body, headers } = ctx.request;
  if (!(await verifySignature(ctx.secrets.PAYMENT_WEBHOOK_SECRET, body, headers["x-signature"]))) {
    return { status: 400, body: "invalid signature" };
  }

  // 2. Deduplicate by the provider's own event id: the unique index on
  //    events.event_id refuses a second insert with a 409.
  const event = JSON.parse(body) as PaymentEvent;
  const db = ctx.admin.db("main");
  try {
    await db.collection("events").create({ event_id: event.id, type: event.type, order_id: event.order_id });
  } catch (err) {
    if ((err as { status?: number }).status === 409) return { status: 200, body: "already processed" };
    throw err;
  }

  // 3. Handle it.
  if (event.type === "payment.succeeded") {
    try {
      await db.collection("orders").patch(event.order_id, { status: "paid" });
    } catch (err) {
      if ((err as { status?: number }).status === 404) return { status: 404, body: "unknown order" };
      throw err;
    }
  }
  return { status: 200, body: "ok" };
}
