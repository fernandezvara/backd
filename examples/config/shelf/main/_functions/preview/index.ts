// preview: given a URL, ask api.microlink.io for the page's title and
// description. `network: [api.microlink.io]` is the whole allowlist — fetch
// anywhere else and the egress refuses the connection; that's the sandbox
// doing the work, not the code.
import type { Context } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { url } = ctx.input as { url: string };

  const res = await fetch(`https://api.microlink.io?url=${encodeURIComponent(url)}`, {
    headers: { accept: "application/json" },
  });
  if (!res.ok) {
    // ctx.error is the caller's answer, so only 4xx exists here; 424 says
    // "the dependency failed", not "the function crashed".
    throw ctx.error(424, "preview_failed", `the preview service answered ${res.status}`);
  }
  const payload = await res.json();
  if (payload.status !== "success") {
    throw ctx.error(424, "preview_failed", payload.error?.message ?? "no metadata for that page");
  }
  const { title, description } = payload.data ?? {};
  return { title: title ?? null, description: description ?? null };
}
