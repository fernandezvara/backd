// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

function assertEquals(got: unknown, want: unknown) {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) throw new Error(`got ${g}, want ${w}`);
}

Deno.test("refund_receipt writes the receipt once, however often it runs", async () => {
  const input = { refund_id: "r1", order_id: "o1", amount: 1250 };
  const { ctx, store } = createContext({ input, admin: true });
  assertEquals(await handler(ctx as never), { written: true });
  assertEquals(store.all("main", "receipts").map((d) => d.text), ["Refund of 12.50 for order o1"]);
});
