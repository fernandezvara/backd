import { defineFunction } from "./backd";
export const price = defineFunction("main/price", async (ctx) => {
  await ctx.db("main").collection("invoices").create({ customer: "ada" });
  return { amount: 1 };
});
