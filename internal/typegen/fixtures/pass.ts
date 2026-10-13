import { defineFunction } from "./backd";

export const place = defineFunction("main/place", async (ctx) => {
  const sku: string = ctx.input.items[0];
  const price = await ctx.call("price", { sku });
  const amount: number = price.amount;
  const orders = ctx.db("main").collection("orders");
  const order = await orders.create({ status: "new", items: [{ sku, qty: 1 }], total: amount });
  const found = await orders.list({ where: { status: "paid", total: { $gte: 100 } }, orderBy: "-total" });
  const first = found.items[0];
  const status: "new" | "paid" | "shipped" = first.status;
  const note: string | null | undefined = order.note;
  const currency = await ctx.config.get("shop.currency");
  const eur: "eur" | "usd" = currency;
  await ctx.admin.db("main").collection("orders").patch(order.id, { status: "paid" });
  return { order_id: order.id + status + note + eur };
});

export const price = defineFunction("main/price", (ctx) => ({ amount: ctx.input.sku.length }));
