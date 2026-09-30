// Attempts the outbound connections F4's egress proxy and network
// placement must refuse in this deployment's own topology, and reports
// what happened instead of throwing, so deploy/production/test.sh can
// check every case from one call. See docker/egress-test/ for the
// exhaustive version of this proof, run in isolation.
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
// itself (layer 3).
async function tryConnect(results, name, hostname, port) {
  try {
    const conn = await withTimeout(Deno.connect({ hostname, port, transport: "tcp" }), 3000);
    conn.close();
    results.push({ test: name, reached: true });
  } catch (e) {
    results.push({ test: name, reached: false, error: String(e?.message ?? e) });
  }
}

export default async function (ctx) {
  const results = [];
  // Layer 1: a host this function never declared.
  await tryFetch(results, "unlisted-host", "http://example.invalid.test/");
  // Layer 2: a declared host that is a cloud-metadata address.
  await tryFetch(results, "metadata", "http://169.254.169.254:80/");
  // Layer 2: a declared name that resolves to MongoDB's real address on
  // this deployment's data network (an /etc/hosts entry set by
  // compose.yaml on the executor and egress containers, mirroring what a
  // misconfigured or attacker-controlled DNS record could do for real).
  await tryFetch(results, "mongo-fetch", "http://mongo-data.internal.test:27017/");
  // Layer 3: the same address, over raw TCP (bypasses egress entirely) —
  // the executor's network has no route to it at all.
  await tryConnect(results, "mongo-rawtcp", "mongo-data.internal.test", 27017);
  return { results };
}
