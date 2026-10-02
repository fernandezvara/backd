// refund: mark a paid order refunded and record the refund, in one
// transaction. `ifMatch` makes the order's patch fail if anyone changed the
// order since we read it, which aborts the whole batch: nothing is written.
// Afterwards it asks the internal refund_receipt function to write the receipt
// and emails the customer, through the realm's `refund-issued` template.
import { relay } from "../lib/relay.ts";
import type { Context, Doc } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { order_id, reason } = ctx.input as { order_id: string; reason?: string };
  const db = ctx.admin.db("main");

  let order: Doc;
  try {
    order = await db.collection("orders").get(order_id);
  } catch (err) {
    relay(err, ctx.error);
  }
  if (order.status !== "paid") {
    throw ctx.error(409, "not_refundable", `An order that is ${order.status} can't be refunded`);
  }

  let refund: Doc;
  try {
    [, refund] = await db.batch([
      { op: "patch", collection: "orders", id: order_id, patch: { status: "refunded" }, ifMatch: order._meta.version },
      {
        op: "create",
        collection: "refunds",
        document: { order_id, amount: order.amount, reason: reason ?? "", refunded_by: ctx.user?.email ?? "api-key" },
      },
    ]);
  } catch (err) {
    relay(err, ctx.error);
  }
  console.log(`refunded order ${order_id}`);

  // The receipt is written by an internal function, as a job: the refund is
  // already done, so a slow or failing receipt never undoes it. The key makes
  // a retried refund queue the same job instead of a second one.
  const job = await ctx.call("refund_receipt", { refund_id: refund.id, order_id, amount: order.amount }, { idempotencyKey: `receipt-${refund.id}` });
  // The customer is told by email: a custom kind of the realm
  // (email/refund-issued/), sent to the order's owner, a user of the realm. The
  // recipient comes from the order, never from the request; a failure here
  // (a limit, a template problem) must not undo or hide a refund that happened.
  try {
    await ctx.email.send({ kind: "refund-issued", to_user: order._meta.owner, data: { order_id, amount: (order.amount / 100).toFixed(2), reason: reason ?? "" } });
  } catch (err) {
    console.log(`the customer was not emailed: ${err instanceof Error ? err.message : err}`);
  }
  return { refund_id: refund.id, order_id, amount: order.amount, receipt_job: job.id };
}
