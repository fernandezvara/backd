// Run with `make functions-testing-test` (Deno; no MongoDB, backd or network).
import { createContext, emailMessage } from "../../../../../../clients/functions-testing/src/index.js";
import handler from "./index.ts";

function assertEquals(got: unknown, want: unknown) {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) throw new Error(`got ${g}, want ${w}`);
}

Deno.test("email-capture stores the message, its link included, in the outbox", async () => {
  const message = emailMessage({ kind: "reset-password", subject: "Reset your password", data: { link: "https://x.example/v1/shelf/_auth/reset-password?token=t1", expires_at: "2030-01-02T15:04:00Z" } });
  const { ctx, store } = createContext({ input: message, admin: true });
  const out = await handler(ctx as never) as { message_id: string };
  const [doc] = store.all("mail", "outbox");
  assertEquals([doc.email_id, doc.kind, doc.to, doc.subject, doc.link], ["email-test", "reset-password", ["ana@example.com"], "Reset your password", "https://x.example/v1/shelf/_auth/reset-password?token=t1"]);
  assertEquals(out.message_id, doc.id);
});

Deno.test("a message without a link is stored with a null link", async () => {
  const { ctx, store } = createContext({ input: emailMessage({ kind: "password-changed", data: {} }), admin: true });
  await handler(ctx as never);
  assertEquals(store.all("mail", "outbox")[0].link, null);
});
