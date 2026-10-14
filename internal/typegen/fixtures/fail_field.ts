import { defineFunction } from "./backd";
export const price = defineFunction("main/price", async (ctx) => {
  await ctx.db("main").collection("orders").create({ status: "refunded", items: [] });
  return { amount: 1 };
});
