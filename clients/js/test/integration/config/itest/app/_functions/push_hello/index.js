// Sends the caller one push; returns what ctx.push.send answered.
export default async (ctx) => {
  return await ctx.push.send({ user: ctx.user.id, kind: 'hello', data: { name: ctx.input.name ?? 'there' }, group: 'hello' })
}
