import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const user = { id: "u1", email: "ada@example.com", email_verified: true, roles: [] };
const orders = [
  { id: "o1", item: "widget", quantity: 2, amount: 1000, status: "paid" },
  { id: "o2", item: "gadget, large", quantity: 1, amount: 500, status: "draft" },
];

Deno.test("exports the orders as a CSV report", async () => {
  const { ctx, store } = createContext({ user, requestId: "req-1" });
  store.seed("main", "orders", orders);
  const out = (await handler(ctx as never)) as { report_id: string; rows: number; reused: boolean };
  if (out.rows !== 2 || out.reused) throw new Error(JSON.stringify(out));
  const [report] = store.all("main", "reports");
  if (report.csv !== 'id,item,quantity,amount,status\no1,widget,2,1000,paid\no2,"gadget, large",1,500,draft\n') throw new Error(report.csv);
  if (report.requested_by !== "ada@example.com" || report.export_key !== "req-1") throw new Error(JSON.stringify(report));
});

Deno.test("running the same job again reuses the report instead of writing another", async () => {
  const store = createContext().store;
  store.seed("main", "orders", orders);
  const first = (await handler(createContext({ user, requestId: "req-1", store }).ctx as never)) as { report_id: string; reused: boolean };
  const second = (await handler(createContext({ user, requestId: "req-1", store }).ctx as never)) as { report_id: string; reused: boolean };
  if (second.report_id !== first.report_id || !second.reused || first.reused) throw new Error(JSON.stringify({ first, second }));
  if (store.all("main", "reports").length !== 1) throw new Error("a second report was written");
  // A different call (another request id) is a different export.
  await handler(createContext({ user, requestId: "req-2", store }).ctx as never);
  if (store.all("main", "reports").length !== 2) throw new Error("a new request should write a new report");
});
