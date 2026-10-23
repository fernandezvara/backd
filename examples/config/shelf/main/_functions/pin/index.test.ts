// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, MemoryStore } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const ana = (roles: string[], claims: Record<string, unknown> = {}, counts = true) => ({
  id: "u1",
  email: "ana@shelf.example",
  roles: [],
  orgs: { o1: { name: "Docs", roles, teams: [], claims, counts, require_mfa: false } },
});

type User = ReturnType<typeof ana>;

function call(user: User, input: object, store = new MemoryStore()) {
  const c = createContext({ store, admin: true, user, input });
  return { store, run: () => Promise.resolve(handler(c.ctx as never)) };
}

/** What a call that must be refused rejects with. */
async function refusal(run: () => Promise<unknown>): Promise<{ status?: number; code?: string }> {
  try {
    await run();
  } catch (e) {
    return e as { status?: number; code?: string };
  }
  throw new Error("it was not refused");
}

Deno.test("an editor pins; the pin is the team's and names its author", async () => {
  const { store, run } = call(ana(["member", "editor"]), { org: "o1", title: "Handbook", url: "https://example.com/handbook" });
  const out = (await run()) as { pins: number; limit: number };
  if (out.pins !== 1 || out.limit !== 5) throw new Error(JSON.stringify(out));
  const [pin] = store.all("main", "pins") as { author: string; title: string }[];
  if (pin.author !== "u1" || pin.title !== "Handbook") throw new Error(JSON.stringify(pin));
});

Deno.test("private notes are written with the pin, for backd to encrypt", async () => {
  const { store, run } = call(ana(["editor"]), { org: "o1", title: "Plan", private_notes: "ask for a raise" });
  await run();
  const [pin] = store.all("main", "pins") as { private_notes: string }[];
  if (pin.private_notes !== "ask for a raise") throw new Error(JSON.stringify(pin));
});

Deno.test("a plain member can't pin, and a stranger is not a member", async () => {
  const member = await refusal(call(ana(["member"]), { org: "o1", title: "x" }).run);
  if (member.status !== 403) throw new Error(JSON.stringify(member));
  const stranger = await refusal(call(ana(["editor"]), { org: "other", title: "x" }).run);
  if (stranger.code !== "not_org_member") throw new Error(JSON.stringify(stranger));
  // A team that requires a second factor doesn't count for a session without one.
  const noMFA = await refusal(call(ana(["editor"], {}, false), { org: "o1", title: "x" }).run);
  if (noMFA.code !== "not_org_member") throw new Error(JSON.stringify(noMFA));
});

Deno.test("the free plan holds five pins, a pro one a hundred", async () => {
  const store = new MemoryStore();
  store.seed("main", "pins", Array.from({ length: 5 }, (_, i) => ({ id: `p${i}`, title: `Pin ${i}`, _meta: { org: "o1" } })));
  const free = await refusal(call(ana(["editor"]), { org: "o1", title: "sixth" }, store).run);
  if (free.code !== "plan_limit") throw new Error(JSON.stringify(free));
  const pro = (await call(ana(["editor"], { plan: "pro" }), { org: "o1", title: "sixth" }, store).run()) as { pins: number; limit: number };
  if (pro.pins !== 6 || pro.limit !== 100) throw new Error(JSON.stringify(pro));
});
