import type { V1, V2 } from "./schema.d.ts";

export default function (doc: V1): V2 {
  return { ...doc, slug: doc.title.toLowerCase().replace(/[^a-z0-9]+/g, "-") };
}
