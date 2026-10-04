// HMAC-SHA256 signatures for webhooks: the sender signs the raw request
// body with a secret you share, and sends it as `x-signature: sha256=<hex>`.
const encoder = new TextEncoder();

async function hmacHex(secret: string, body: string): Promise<string> {
  const key = await crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const mac = await crypto.subtle.sign("HMAC", key, encoder.encode(body));
  return [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/** What a sender puts in the header (used by tests and by the docs' curl example). */
export async function sign(secret: string, body: string): Promise<string> {
  return `sha256=${await hmacHex(secret, body)}`;
}

/** True when `header` is the signature of `body` under `secret`. Compares in constant time. */
export async function verifySignature(secret: string, body: string, header: string | undefined): Promise<boolean> {
  if (!header) return false;
  const expected = encoder.encode(await sign(secret, body));
  const given = encoder.encode(header);
  if (expected.length !== given.length) return false;
  let diff = 0;
  for (let i = 0; i < expected.length; i++) diff |= expected[i] ^ given[i];
  return diff === 0;
}
