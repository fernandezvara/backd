// order_total: the amount a customer will be charged for one of their
// orders. The tax rule lives here, on the server, not in every client.
//
// ctx.db acts as the caller, so orders/collection.yaml decides what they may
// read: someone else's order (or one that doesn't exist) answers 404, the
// same as reading the collection directly would.
import { relay } from "../lib/relay.ts";
import type { Context, Doc } from "../lib/types.ts";

const TAX_RATE = 0.2;

export default async function handler(ctx: Context) {
  const { order_id } = ctx.input as { order_id: string };

  let order: Doc;
  try {
    order = await ctx.db("main").collection("orders").get(order_id);
  } catch (err) {
    relay(err, ctx.error);
  }
  if (order.status === "refunded") {
    throw ctx.error(409, "order_refunded", "This order was refunded");
  }

  const subtotal = order.amount as number;
  const tax = Math.round(subtotal * TAX_RATE);
  return { order_id, subtotal, tax, total: subtotal + tax };
}
