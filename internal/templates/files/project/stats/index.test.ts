// Run with `deno test` (or `make functions-test`, see the README) —
// no MongoDB, no running backd, no network. See ../lib/testing.js for
// what the fake ctx does and doesn't do.
import { createContext } from "../lib/testing.js";
import handler from "./index.ts";

function assertEquals(got: unknown, want: unknown) {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) throw new Error(`got ${g}, want ${w}`);
}

Deno.test("counts published posts and the caller's own drafts", async () => {
  const { ctx, store } = createContext({ user: { id: "u1", email: "ada@example.com", email_verified: true, roles: [] } });
  store.seed("main", "posts", [
    { title: "a", published: true },
    { title: "b", published: true },
    { title: "c", published: false, _meta: { owner: "u1" } },
    { title: "d", published: false, _meta: { owner: "u2" } },
  ]);

  const out = await handler(ctx);

  assertEquals(out, { published: 2, mine: 1 });
});

Deno.test("an anonymous caller sees published counts but no drafts", async () => {
  const { ctx, store } = createContext();
  store.seed("main", "posts", [{ title: "a", published: true }]);

  const out = await handler(ctx);

  assertEquals(out, { published: 1, mine: 0 });
});
