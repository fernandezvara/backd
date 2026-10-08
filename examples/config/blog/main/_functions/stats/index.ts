// stats: post counts a reader can't compute themselves — the total
// published count needs every published post, and "your drafts" needs
// posts the caller couldn't list without being their author, since
// posts/collection.yaml only lets a reader list what they're allowed to see.
//
// Build it with `backd functions build` after every change, or run
// `backd serve` with BACKD_DEV=true while you edit (see the docs).

type Context = {
  db: (name: string) => { collection: (name: string) => Collection };
  user: { id: string; email: string; email_verified: boolean; roles: string[] } | null;
};

type Collection = {
  list: (params: { where?: unknown; limit?: number; count?: boolean }) => Promise<{ total?: number }>;
};

export default async function handler(ctx: Context) {
  const posts = ctx.db("main").collection("posts");
  const published = await posts.list({ where: { published: true }, limit: 1, count: true });
  const mine = ctx.user
    ? await posts.list({ where: { "_meta.owner": ctx.user.id }, limit: 1, count: true })
    : { total: 0 };
  return { published: published.total ?? 0, mine: mine.total ?? 0 };
}
