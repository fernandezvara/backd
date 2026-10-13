import { defineFunction } from "./backd";
// price is not an admin function: no ctx.admin
export const price = defineFunction("main/price", async (ctx) => {
  await ctx.admin.db("main").collection("orders").list();
  return { amount: 1 };
});
