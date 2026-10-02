// What a payment provider does: signs the raw body of a webhook with a secret
// it shares with you, HMAC-SHA256, and sends it as `x-signature: sha256=<hex>`.
// The page plays the provider here, so the secret is in the page; a real
// provider keeps it on its own servers and you keep yours in backd's secrets.

const encoder = new TextEncoder()

/**
 * @param {string} secret
 * @param {string} body
 * @returns {Promise<string>} `sha256=<hex>`
 */
export async function signBody(secret, body) {
  const key = await crypto.subtle.importKey('raw', encoder.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  const mac = await crypto.subtle.sign('HMAC', key, encoder.encode(body))
  return `sha256=${[...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, '0')).join('')}`
}
