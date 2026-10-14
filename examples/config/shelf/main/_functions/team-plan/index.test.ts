// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

Deno.test("it puts a team on a plan, as a claim of its organization", async () => {
  const c = createContext({ admin: true, orgs: { orgs: [{ id: "o1", name: "Docs" }] }, input: { org: "o1", plan: "pro" } });
  const out = (await handler(c.ctx as never)) as { plan: string };
  if (out.plan !== "pro") throw new Error(JSON.stringify(out));
  const org = (await c.ctx.admin.orgs.get("o1")) as { claims: { plan: string } };
  if (org.claims.plan !== "pro") throw new Error(JSON.stringify(org));
  if (c.orgChanges().map((x: { action: string }) => x.action).join() !== "setClaims") throw new Error("not recorded");
});

Deno.test("an unknown team is a 404", async () => {
  const c = createContext({ admin: true, orgs: true, input: { org: "nope", plan: "pro" } });
  let status: number | undefined;
  try {
    await handler(c.ctx as never);
  } catch (e) {
    status = (e as { status?: number }).status;
  }
  if (status !== 404) throw new Error(`status ${status}`);
});
