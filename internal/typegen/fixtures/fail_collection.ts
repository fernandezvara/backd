import { defineFunction } from "./backd";
export const price = defineFunction("main/price", async (ctx) => {
  await ctx.db("main").collection("ledgers").list();
  return { amount: 1 };
});
