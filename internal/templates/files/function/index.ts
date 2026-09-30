// A backd function. The default export is called once per call with the
// call's context and returns the output (any JSON value).
//
//   ctx.input   the request body (checked against input.schema.json, if any)
//   ctx.user    the caller ({ id, email, email_verified, roles }), or null
//
// Build it with `backd functions build` after every change.

type Context = {
  input: unknown;
  user: { id: string; email: string; email_verified: boolean; roles: string[] } | null;
};

export default async function handler(ctx: Context) {
  return { hello: ctx.user?.email ?? "anonymous" };
}
