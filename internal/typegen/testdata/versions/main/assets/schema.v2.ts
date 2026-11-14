import type { V1, V2, Meta } from "./schema.d.ts";

export default function (doc: V1, meta: Meta): V2 {
  return {
    ...doc,
    description: doc.body ?? "",
    visibility: doc.published_at ? "public" : meta.org ? "team" : "public",
  };
}
