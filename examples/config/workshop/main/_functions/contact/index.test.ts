import { createContext } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

const user = { id: "u1", email: "ada@example.com", email_verified: true, roles: [] };

Deno.test("writes to the fixed support address, with the caller as the sender", async () => {
  const { ctx, sentEmails } = createContext({ user, email: true, input: { message: "Where is my order?" } });
  const out = (await handler(ctx as never)) as { sent: boolean; email_job: string };
  if (!out.sent || !out.email_job) throw new Error(JSON.stringify(out));
  const [mail] = sentEmails();
  if (mail.kind !== "contact-message" || mail.to.join() !== "support@workshop.example" || mail.cc || mail.bcc) throw new Error(JSON.stringify(mail));
  if (mail.data.from !== "ada@example.com" || mail.data.message !== "Where is my order?") throw new Error(JSON.stringify(mail.data));
});

Deno.test("the recipient never comes from the input, whatever the input says", async () => {
  // The input schema refuses extra fields; even if one got through, the code ignores it.
  const { ctx, sentEmails } = createContext({ user, email: true, input: { message: "hi", to: ["victim@example.org"], cc: ["x@example.org"] } });
  await handler(ctx as never);
  const [mail] = sentEmails();
  if (mail.to.join() !== "support@workshop.example" || mail.cc !== undefined) throw new Error(JSON.stringify(mail));
});
