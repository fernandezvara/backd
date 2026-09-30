// add_expense: creates an expense only after checking group_id and
// split_between against the group's REAL, current members — not a
// client-supplied copy, which is what let hole 1 in "expenses without
// functions" happen (anyone who knew a group's id could write into it).
//
// Reading the group through ctx.db, acting as the caller, does double
// duty: groups/rules.yaml's read rule already requires the caller's email
// to be a current member, so a non-member's read fails not_found, which
// refuses the whole call before any expense is written.
import { relay } from "../lib/relay.ts";

type Context = {
  db: (name: string) => Database;
  user: { id: string; email: string } | null;
  input: unknown;
  error: (status: number, code: string, message: string) => Error;
};

type Database = { collection: (name: string) => Collection };

type Collection = {
  get: (id: string) => Promise<Record<string, unknown>>;
  create: (doc: Record<string, unknown>) => Promise<Record<string, unknown>>;
};

type Input = {
  group_id: string;
  description?: string;
  amount: number;
  split_between: string[];
};

export default async function handler(ctx: Context) {
  const input = ctx.input as Input;
  if (!input?.group_id || !Array.isArray(input.split_between) || input.split_between.length === 0) {
    throw ctx.error(400, "validation_error", "group_id and a non-empty split_between are required");
  }
  if (!Number.isInteger(input.amount) || input.amount <= 0) {
    throw ctx.error(400, "validation_error", "amount must be a positive integer number of cents");
  }

  const db = ctx.db("main");
  // 404 here (not_found) for an unknown group, or a group the caller
  // isn't currently a member of: both look the same to the caller, same
  // as reading the group directly would (relay: see lib/relay.ts).
  let group: Record<string, unknown>;
  try {
    group = await db.collection("groups").get(input.group_id);
  } catch (err) {
    relay(err, ctx.error);
  }
  const members = group.members as string[];
  for (const email of input.split_between) {
    if (!members.includes(email)) {
      throw ctx.error(422, "not_a_member", `${email} is not a current member of this group`);
    }
  }

  try {
    return await db.collection("expenses").create({
      kind: "expense",
      group_id: input.group_id,
      description: input.description ?? "",
      amount: input.amount,
      paid_by: ctx.user!.email,
      split_between: input.split_between,
    });
  } catch (err) {
    relay(err, ctx.error);
  }
}
