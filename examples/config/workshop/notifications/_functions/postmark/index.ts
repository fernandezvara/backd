// postmark: delivers each email backd renders through Postmark's HTTP API.
//
// ctx.input is the finished message (see the docs, Functions -> Email -> The
// delivery function). The function returns Postmark's MessageID, which backd
// keeps on the email's job, and ends the way backd expects:
//   - Postmark refuses the message itself (a malformed or inactive address, a
//     sender it doesn't know): a 4xx FunctionError, so the email is not retried;
//   - anything else that goes wrong (Postmark down, rate limited, the network,
//     a wrong token): a plain error, so backd retries it per `retry`.
type Message = {
  id: string;
  kind: string;
  from: string;
  reply_to: string | null;
  to: { email: string; name: string | null }[];
  cc: { email: string; name: string | null }[];
  bcc: { email: string; name: string | null }[];
  subject: string;
  text: string;
  html: string;
};

type Context = {
  input: unknown;
  secrets: Record<string, string>;
  error(status: number, code: string, message: string): Error;
};

const API = "https://api.postmarkapp.com/email";

// "Ana <ana@example.com>", or the bare address.
const address = (a: { email: string; name: string | null }) => (a.name ? `${a.name} <${a.email}>` : a.email);

export default async function handler(ctx: Context) {
  const m = ctx.input as Message;
  const res = await fetch(API, {
    method: "POST",
    headers: {
      "X-Postmark-Server-Token": ctx.secrets.POSTMARK_TOKEN,
      "Content-Type": "application/json",
      Accept: "application/json",
    },
    body: JSON.stringify({
      // The sender Postmark verified; email.from in realm.yaml should match it.
      From: ctx.secrets.POSTMARK_FROM,
      To: m.to.map(address).join(","),
      Cc: m.cc.length ? m.cc.map(address).join(",") : undefined,
      Bcc: m.bcc.length ? m.bcc.map(address).join(",") : undefined,
      ReplyTo: m.reply_to ?? undefined,
      Subject: m.subject,
      TextBody: m.text,
      HtmlBody: m.html,
      // For Postmark's own reports: the kind of email, and which backd email it was.
      Tag: m.kind,
      Metadata: { backd_email_id: m.id },
      MessageStream: "outbound",
    }),
    signal: AbortSignal.timeout(30_000),
  });

  const body = await res.json().catch(() => ({})) as { MessageID?: string; ErrorCode?: number; Message?: string };
  // 422: Postmark understood the request and refuses the message (ErrorCode 300
  // invalid email, 406 inactive recipient, 400 unknown sender, …). Retrying
  // can't change that.
  if (res.status === 422) {
    throw ctx.error(422, `postmark_${body.ErrorCode ?? "rejected"}`, body.Message ?? "Postmark rejected the message");
  }
  // Anything else that isn't a success is worth another attempt (or, for a
  // wrong token, a visible failure in the job list until it is fixed).
  if (!res.ok) throw new Error(`Postmark answered ${res.status}: ${body.Message ?? "no message"}`);
  console.log(`sent ${m.kind} email ${m.id} through Postmark`);
  return { message_id: body.MessageID };
}
