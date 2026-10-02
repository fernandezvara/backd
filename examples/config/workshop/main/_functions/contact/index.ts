// contact: a customer writes to the shop. The email goes to one fixed address,
// the shop's own mailbox, so nobody can use this function to write to someone
// else; the caller's text only ever reaches that mailbox.
import type { Context } from "../lib/types.ts";

// Fixed in the code: never read from the input.
const SUPPORT = "support@workshop.example";

export default async function handler(ctx: Context) {
  const { message } = ctx.input as { message: string };
  const job = await ctx.email.send({
    kind: "contact-message",
    to: [SUPPORT],
    data: { from: ctx.user?.email ?? "", message },
  });
  return { sent: true, email_job: job.id };
}
