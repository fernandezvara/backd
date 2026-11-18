// main/assets, v2 → v3 (contract): body goes; description is the only text.
import type { V2, V3 } from "./schema.d.ts";

export default function (doc: V2): V3 {
  const { body, ...rest } = doc;
  return { ...rest, description: doc.description ?? body ?? "", visibility: doc.visibility ?? "team" };
}
