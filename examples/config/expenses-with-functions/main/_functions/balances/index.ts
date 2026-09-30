// balances: the one authoritative balance per group, computed here
// instead of by each client (hole 4 in "expenses without functions",
// where every client computed its own from data that holes 1-3 let
// anyone forge). A pending settlement (../request_settlement, not yet
// confirmed by ../confirm_settlement) doesn't count yet: only a receiver
// agreeing it happened makes it real.
//
// The arithmetic mirrors clients/js/examples/expenses-without-functions/
// ledger.js on purpose — same remainder handling, same greedy pairing —
// so the two examples' "to settle up" lists read identically; only who
// computes them, and what they're computed from, differs.
import { relay } from "../lib/relay.ts";

type Context = {
  db: (name: string) => Database;
  admin: { db: (name: string) => Database };
  input: unknown;
  error: (status: number, code: string, message: string) => Error;
};

type Database = { collection: (name: string) => Collection };

type Collection = {
  get: (id: string) => Promise<Record<string, unknown>>;
  list: (params: { where?: unknown; limit?: number }) => Promise<{ items: Entry[] }>;
};

type Entry = { kind: string; amount: number; paid_by: string; split_between: string[]; status?: string };
type Input = { group_id: string };

function balances(entries: Entry[]): Record<string, number> {
  const net: Record<string, number> = {};
  const add = (who: string, cents: number) => (net[who] = (net[who] ?? 0) + cents);
  for (const e of entries) {
    add(e.paid_by, e.amount);
    const n = e.split_between.length;
    const share = Math.floor(e.amount / n);
    let rest = e.amount - share * n;
    for (const who of e.split_between) {
      add(who, -(share + (rest > 0 ? 1 : 0)));
      rest--;
    }
  }
  return net;
}

function settlementPlan(net: Record<string, number>) {
  const debtors = Object.entries(net).filter(([, v]) => v < 0).map(([who, v]) => ({ who, v: -v }));
  const creditors = Object.entries(net).filter(([, v]) => v > 0).map(([who, v]) => ({ who, v }));
  debtors.sort((a, b) => b.v - a.v || a.who.localeCompare(b.who));
  creditors.sort((a, b) => b.v - a.v || a.who.localeCompare(b.who));
  const plan: { from: string; to: string; amount: number }[] = [];
  let i = 0, j = 0;
  while (i < debtors.length && j < creditors.length) {
    const amount = Math.min(debtors[i].v, creditors[j].v);
    plan.push({ from: debtors[i].who, to: creditors[j].who, amount });
    debtors[i].v -= amount;
    creditors[j].v -= amount;
    if (debtors[i].v === 0) i++;
    if (creditors[j].v === 0) j++;
  }
  return plan;
}

export default async function handler(ctx: Context) {
  const { group_id } = (ctx.input ?? {}) as Input;
  if (!group_id) throw ctx.error(400, "validation_error", "group_id is required");

  try {
    await ctx.db("main").collection("groups").get(group_id); // 404: unknown group, or not a current member
  } catch (err) {
    relay(err, ctx.error);
  }

  const { items } = await ctx.admin.db("main").collection("expenses").list({ where: { group_id }, limit: 100 });
  const counted = items.filter((e) => e.kind === "expense" || e.status === "confirmed");
  const net = balances(counted);
  return { balances: net, plan: settlementPlan(net) };
}
