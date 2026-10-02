// email-capture: stores each email backd renders in the `outbox` collection
// instead of sending it. Local development only (see function.yaml).
type Message = {
  id: string;
  kind: string;
  from: string;
  to: { email: string }[];
  subject: string;
  text: string;
  html: string;
  locale: string;
  data: { link?: string; expires_at?: string };
};

export default async function handler(ctx: { input: unknown; admin: { db(name: string): any } }) {
  const m = ctx.input as Message;
  const doc = await ctx.admin.db("__DATABASE__").collection("outbox").create({
    email_id: m.id,
    kind: m.kind,
    from: m.from,
    to: m.to.map((t) => t.email),
    subject: m.subject,
    text: m.text,
    html: m.html,
    locale: m.locale,
    link: m.data?.link ?? null,
  });
  console.log(`captured ${m.kind} email for ${m.to.map((t) => t.email).join(", ")}`);
  return { message_id: doc.id };
}
