// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { toCsv } from "./csv.ts";
import { sign, verifySignature } from "./signature.ts";

function assertEquals(got: unknown, want: unknown) {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) throw new Error(`got ${g}, want ${w}`);
}

Deno.test("toCsv writes a header and quotes what needs quoting", () => {
  const csv = toCsv([{ id: "a", item: 'widget, "large"', quantity: 2 }, { id: "b", item: "plain", quantity: null }], ["id", "item", "quantity"]);
  assertEquals(csv, 'id,item,quantity\na,"widget, ""large""",2\nb,plain,\n');
});

Deno.test("toCsv of no rows is just the header", () => {
  assertEquals(toCsv([], ["id", "item"]), "id,item\n");
});

Deno.test("a signature verifies only for the same body and secret", async () => {
  const body = '{"id":"evt_1","type":"payment.succeeded","order_id":"o1"}';
  const header = await sign("s3cret", body);
  assertEquals(header.startsWith("sha256="), true);
  assertEquals(await verifySignature("s3cret", body, header), true);
  assertEquals(await verifySignature("s3cret", body + " ", header), false);
  assertEquals(await verifySignature("other", body, header), false);
  assertEquals(await verifySignature("s3cret", body, undefined), false);
  assertEquals(await verifySignature("s3cret", body, "sha256=00"), false);
});
