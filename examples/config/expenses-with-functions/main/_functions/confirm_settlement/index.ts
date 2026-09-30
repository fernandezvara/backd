// confirm_settlement: the receiver of a pending settlement confirms it
// really happened. Until this runs, the entry has status "pending" and
// ../balances ignores it — closing hole 3 ("Unconfirmed settlements")
// from "expenses without functions", where the payer's own record was
// final.
//
// ctx.admin.db is required for two reasons at once: reading the entry at
// all (the direct read rule only returns your own; the receiver isn't the
// writer) and writing its status (the update rule is owner-only too, and
// confirming is never the owner's job — that asymmetry is the whole
// point: only this function, not a PATCH the receiver could send
// themselves, may flip status).
import { relay } from "../lib/relay.ts";

type Context = {
  admin: { db: (name: string) => Database };
  user: { id: string; email: string } | null;
  input: unknown;
  error: (status: number, code: string, message: string) => Error;
};

type Database = { collection: (name: string) => Collection };

type Entry = {
  id: string;
  kind: string;
  status?: string;
  split_between: string[];
  _meta: { version: number };
};

type Collection = {
  get: (id: string) => Promise<Entry>;
  patch: (id: string, patch: Record<string, unknown>, opts: { ifMatch: number }) => Promise<Entry>;
};

type Input = { id: string };

export default async function handler(ctx: Context) {
  const { id } = (ctx.input ?? {}) as Input;
  if (!id) throw ctx.error(400, "validation_error", "id is required");

  const expenses = ctx.admin.db("main").collection("expenses");
  let entry: Entry;
  try {
    entry = await expenses.get(id);
  } catch (err) {
    relay(err, ctx.error);
  }
  if (entry.kind !== "settlement" || entry.status !== "pending") {
    throw ctx.error(422, "not_pending", "this entry isn't a pending settlement");
  }
  if (entry.split_between[0] !== ctx.user!.email) {
    throw ctx.error(403, "not_the_receiver", "only the person the settlement was paid to may confirm it");
  }

  try {
    return await expenses.patch(id, { status: "confirmed" }, { ifMatch: entry._meta.version });
  } catch (err) {
    relay(err, ctx.error);
  }
}
