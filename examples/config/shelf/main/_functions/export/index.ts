// export: the caller's own assets, as the app saves them (chapter 18).
// The data calls run as the caller, so the assets' rules still decide what is
// readable; the invoke rule in function.yaml is what the switch controls.
import type { Context } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const rows = [];
  for await (const asset of ctx.db("main").collection("assets").iterate({
    where: { "_meta.owner": ctx.user!.id },
    orderBy: "-_meta.created_at",
    limit: 100,
  })) {
    rows.push({
      id: asset.id,
      title: asset.title,
      kind: asset.kind,
      url: asset.url ?? null,
      tags: asset.tags ?? [],
      published_at: asset.published_at ?? null,
    });
  }
  return { exported_at: new Date().toISOString(), count: rows.length, assets: rows };
}
