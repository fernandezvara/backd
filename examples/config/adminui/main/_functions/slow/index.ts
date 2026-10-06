// slow: waits, so there is something to cancel, and reports where it is.
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

export default async function handler(ctx: { step(name: string, options?: { total?: number; message?: string }): void; progress(current: number, message?: string): void }) {
  ctx.step("warm up", { message: "getting ready" });
  await sleep(2_000);
  ctx.step("count", { total: 5 });
  for (let i = 1; i <= 5; i++) {
    await sleep(4_500);
    ctx.progress(i, `item ${i} of 5`);
  }
  return { slept: true };
}
