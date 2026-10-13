import { defineFunction } from "./backd";
export const place = defineFunction("main/place", async (ctx) => {
  const n: number = ctx.input.items; // items is string[]
  return { order_id: String(n) };
});
