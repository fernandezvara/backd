// refund_receipt: writes the receipt of a refund. Called by refund with
// ctx.call, as a job that runs at least once: the unique index on
// receipts.refund_id turns a repeated run into "already written".
import type { Context } from "../lib/types.ts";

type Input = { refund_id: string; order_id: string; amount: number };

export default async function handler(ctx: Context) {
  const { refund_id, order_id, amount } = ctx.input as Input;
  const text = `Refund of ${(amount / 100).toFixed(2)} for order ${order_id}`;
  try {
    await ctx.admin.db("main").collection("receipts").create({ refund_id, order_id, text });
  } catch (err) {
    if ((err as { status?: number }).status === 409) return { written: false, reason: "already written" };
    throw err;
  }
  console.log(`receipt written for refund ${refund_id}`);
  return { written: true };
}
