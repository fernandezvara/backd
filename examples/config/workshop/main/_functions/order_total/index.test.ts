import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const user = { id: "u1", email: "ada@example.com", email_verified: true, roles: [] };

Deno.test("adds 20% tax to the order's amount", async () => {
  const { ctx, store } = createContext({ user, input: { order_id: "o1" } });
  store.seed("main", "orders", [{ id: "o1", item: "widget", quantity: 1, amount: 1000, status: "paid" }]);
  const out = await handler(ctx as never);
  if (JSON.stringify(out) !== JSON.stringify({ order_id: "o1", subtotal: 1000, tax: 200, total: 1200 })) throw new Error(JSON.stringify(out));
});

Deno.test("a refunded order is a 409", async () => {
  const { ctx, store } = createContext({ user, input: { order_id: "o1" } });
  store.seed("main", "orders", [{ id: "o1", item: "widget", quantity: 1, amount: 1000, status: "refunded" }]);
  try {
    await handler(ctx as never);
  } catch (e) {
    if ((e as { status: number; code: string }).status !== 409 || (e as { code: string }).code !== "order_refunded") throw e;
    return;
  }
  throw new Error("expected order_refunded");
});

Deno.test("an unknown order is a 404, relayed from the client's error", async () => {
  const { ctx } = createContext({ user, input: { order_id: "nope" } });
  try {
    await handler(ctx as never);
  } catch (e) {
    if ((e as { status: number }).status !== 404) throw e;
    return;
  }
  throw new Error("expected a 404");
});
