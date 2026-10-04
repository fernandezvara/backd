// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
//
// The webhook handler itself isn't unit-testable with the fake context (it
// needs ctx.request) — the signature is the part worth proving anyway.
import { sign, verifySignature } from "./signature.ts";

Deno.test("sign produces the sha256=<hex> a sender would send", async () => {
  const sig = await sign("secret-1", `{"event_id":"e1"}`);
  if (!sig.startsWith("sha256=") || sig.length !== 71) throw new Error(sig);
});

Deno.test("verify accepts a good signature, rejects a tampered body and a wrong secret", async () => {
  const body = `{"event_id":"e1","title":"Handbook"}`;
  const sig = await sign("secret-1", body);
  if (!(await verifySignature("secret-1", body, sig))) throw new Error("rejected a valid signature");
  if (await verifySignature("secret-1", body + " ", sig)) throw new Error("accepted a tampered body");
  if (await verifySignature("other", body, sig)) throw new Error("accepted a wrong secret");
  if (await verifySignature("secret-1", body, undefined)) throw new Error("accepted a missing header");
});
