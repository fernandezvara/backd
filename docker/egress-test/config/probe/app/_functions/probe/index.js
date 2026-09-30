// Attempts the outbound connections F4's egress proxy and network
// placement must refuse, and reports what happened instead of throwing,
// so docker/egress-test/test.sh can check every case from one call.
const withTimeout = (p, ms) =>
  Promise.race([p, new Promise((_, reject) => setTimeout(() => reject(new Error("probe timeout")), ms))]);

async function tryFetch(results, name, url) {
  try {
    const res = await withTimeout(fetch(url), 3000);
    results.push({ test: name, reached: true, status: res.status });
  } catch (e) {
    results.push({ test: name, reached: false, error: String(e?.message ?? e) });
  }
}

// tryConnect opens a raw TCP connection (Deno.connect), which bypasses
// HTTP_PROXY entirely: the only thing that can refuse it is the network
// itself (layer 3) or, for the executor's own loopback, its own auth.
async function tryConnect(results, name, hostname, port) {
  try {
    const conn = await withTimeout(Deno.connect({ hostname, port, transport: "tcp" }), 3000);
    conn.close();
    results.push({ test: name, reached: true });
  } catch (e) {
    results.push({ test: name, reached: false, error: String(e?.message ?? e) });
  }
}

// tryUnauthenticatedInvoke raw-TCP-connects (as tryConnect does) and, if
// that succeeds, sends an unauthenticated request over it: the executor's
// own /invoke needs the shared executor token, so reaching the socket
// must still get nothing.
async function tryUnauthenticatedInvoke(results, name, hostname, port) {
  try {
    const conn = await withTimeout(Deno.connect({ hostname, port, transport: "tcp" }), 3000);
    const enc = new TextEncoder();
    await conn.write(enc.encode(`POST /invoke HTTP/1.1\r\nHost: ${hostname}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n`));
    const buf = new Uint8Array(512);
    const n = await withTimeout(conn.read(buf), 3000);
    conn.close();
    const statusLine = new TextDecoder().decode(buf.subarray(0, n ?? 0)).split("\r\n")[0] ?? "";
    results.push({ test: name, reached: true, status_line: statusLine });
  } catch (e) {
    results.push({ test: name, reached: false, error: String(e?.message ?? e) });
  }
}

export default async function (ctx) {
  const results = [];
  // Layer 1: a host the function never declared.
  await tryFetch(results, "unlisted-host", "http://example.invalid.test/");
  // Layer 2: a declared host that is a cloud-metadata address.
  await tryFetch(results, "metadata", "http://169.254.169.254:80/");
  // Layer 2: a declared name that resolves to MongoDB's real address.
  await tryFetch(results, "mongo-clone-fetch", "http://mongo-clone.test:27017/");
  // Layer 3: the same, over raw TCP (bypasses the proxy); the executor's
  // network has no route to MongoDB's at all.
  await tryConnect(results, "mongo-clone-rawtcp", "mongo-clone.test", 27017);
  // Layer 2: a declared name that resolves to a loopback address.
  await tryFetch(results, "sneaky-loopback-fetch", "http://sneaky.test:9100/");
  // Raw TCP to that same name reaches the executor's own loopback (no
  // network placement can prevent a container reaching itself); its
  // /invoke must still refuse an unauthenticated request.
  await tryUnauthenticatedInvoke(results, "sneaky-executor-rawtcp", "sneaky.test", 9100);
  return { results };
}
