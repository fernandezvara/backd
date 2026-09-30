// refund: mark a paid order refunded and record the refund, in one
// transaction. `ifMatch` makes the order's patch fail if anyone changed the
// order since we read it, which aborts the whole batch: nothing is written.
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
  return { refund_id: refund.id, order_id, amount: order.amount };
}
