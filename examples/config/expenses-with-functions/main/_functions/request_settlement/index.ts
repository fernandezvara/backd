// request_settlement: records that the caller says they paid someone
// back, as status "pending" — it takes effect (counts in balances) only
// once the receiver confirms it with confirm_settlement. In "expenses
// without functions", the payer's own record was the final word (hole 3);
// here it's just a claim until the other person agrees.
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

type Input = { group_id: string; to: string; amount: number };

export default async function handler(ctx: Context) {
  const input = ctx.input as Input;
  if (!input?.group_id || !input.to) throw ctx.error(400, "validation_error", "group_id and to are required");
  if (!Number.isInteger(input.amount) || input.amount <= 0) {
    throw ctx.error(400, "validation_error", "amount must be a positive integer number of cents");
  }
  if (input.to === ctx.user!.email) throw ctx.error(422, "cant_settle_with_yourself", "to must be someone else");

  const db = ctx.db("main");
  let group: Record<string, unknown>;
  try {
    group = await db.collection("groups").get(input.group_id); // 404: unknown group, or not a current member
  } catch (err) {
    relay(err, ctx.error);
  }
  if (!(group.members as string[]).includes(input.to)) {
    throw ctx.error(422, "not_a_member", `${input.to} is not a current member of this group`);
  }

  try {
    return await db.collection("expenses").create({
      kind: "settlement",
      group_id: input.group_id,
      description: "Settlement",
      amount: input.amount,
      paid_by: ctx.user!.email,
      split_between: [input.to],
      status: "pending",
    });
  } catch (err) {
    relay(err, ctx.error);
  }
}
