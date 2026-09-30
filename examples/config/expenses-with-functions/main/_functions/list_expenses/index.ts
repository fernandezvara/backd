// list_expenses: the only way to see a group's *other* members' expenses
// in this version — a direct read (expenses/rules.yaml) only ever returns
// your own. Checking membership here, fresh against the group every time,
// is what closes hole 2 ("Stale member copies") — a removed member fails
// the group read and gets nothing; a new member sees every expense
// immediately, with no copy to refresh.
//
// ctx.admin.db is needed to see entries owned by other people (the direct
// read rule is deliberately narrower: only your own); the membership
// check right before it is what actually decides who may see what, not
// the (deliberately unconditional) admin access.
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
  list: (params: { where?: unknown; limit?: number; orderBy?: string }) => Promise<{ items: Record<string, unknown>[] }>;
};

type Input = { group_id: string };

export default async function handler(ctx: Context) {
  const { group_id } = (ctx.input ?? {}) as Input;
  if (!group_id) throw ctx.error(400, "validation_error", "group_id is required");

  // 404 for an unknown group or a non-member, exactly like reading it
  // directly would (relay: see lib/relay.ts).
  try {
    await ctx.db("main").collection("groups").get(group_id);
  } catch (err) {
    relay(err, ctx.error);
  }

  const { items } = await ctx.admin.db("main").collection("expenses").list({
    where: { group_id },
    orderBy: "-_meta.created_at",
    limit: 100,
  });
  return { items };
}
