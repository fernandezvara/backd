import { defineFunction } from "./backd";
// price doesn't declare calls: and place isn't callable from it
export const price = defineFunction("main/price", async (ctx) => {
  await ctx.call("place", { items: [] });
  return { amount: 1 };
});
