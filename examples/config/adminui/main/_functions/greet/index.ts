// greet: says hello to ctx.input.name, as the user the administrator runs it as.
interface Ctx {
  input: { name?: string } | null;
  user: { email: string } | null;
}

export default function handler(ctx: Ctx) {
  const name = ctx.input?.name ?? "world";
  console.log(`greeting ${name}`);
  return { message: `hello ${name}`, as: ctx.user?.email ?? null };
}
