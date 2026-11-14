import type { V2, V3 } from "./schema.d.ts";

export default function (doc: V2): V3 {
  const { body: _body, ...rest } = doc;
  return { ...rest, visibility: doc.visibility ?? "public" };
}
