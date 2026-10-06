// export_orders: build a CSV of the caller's orders and store it as a
// `reports` document; the job's result is the report's id.
//
// A job is delivered at least once: if a worker dies mid-run, another runs
// the job again. So the function must be safe to run twice. The request id
// of the call that queued the job is the same on every attempt, so it makes
// a stable key: the second attempt finds the report the first one wrote and
// returns it instead of writing another.
import { toCsv } from "../lib/csv.ts";
import type { Context, Doc } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const key = ctx.idempotencyKey ?? ctx.requestId;
  const db = ctx.db("main");
  const reports = db.collection("reports");

  const existing = await reports.list({ where: { export_key: key }, limit: 1 });
  if (existing.items.length > 0) {
    const report = existing.items[0];
    return { report_id: report.id, rows: report.rows as number, reused: true };
  }

  ctx.step("read the orders");
  const orders: Doc[] = [];
  for await (const order of db.collection("orders").iterate({ orderBy: "_meta.created_at" })) {
    orders.push(order);
  }

  ctx.step("write the report", { total: orders.length });
  const report = await reports.create({
    kind: "orders",
    export_key: key,
    rows: orders.length,
    csv: toCsv(orders, ["id", "item", "quantity", "amount", "status"]),
    requested_by: ctx.user!.email,
  });
  ctx.progress(orders.length, "report stored");
  console.log(`exported ${orders.length} orders for ${ctx.user!.email}`);
  return { report_id: report.id, rows: orders.length, reused: false };
}
