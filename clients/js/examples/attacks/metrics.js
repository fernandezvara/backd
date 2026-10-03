// Checks what the metrics expose, on the local stack: the metrics port is
// private (nothing on the public address serves it) and the Prometheus the
// stack runs scrapes every process and holds nothing sensitive in its labels.
//
//   make example                   # in another terminal
//   NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/attacks/metrics.js
//
// PROMETHEUS_URL overrides Prometheus (default http://localhost:9090).
const url = process.env.BACKD_URL ?? 'https://localhost:8443'
const prometheus = process.env.PROMETHEUS_URL ?? 'http://localhost:9090'

let unexpected = 0
/** @param {string} what @param {boolean} ok @param {unknown} [detail] */
function check(what, ok, detail) {
  if (!ok) unexpected++
  console.log(`  ${ok ? '✓ ok         ' : '✗ UNEXPECTED '} ${what}${ok ? '' : `\n                 got: ${typeof detail === 'string' ? detail : JSON.stringify(detail)}`}`)
}

console.log('The public address:')
for (const path of ['/metrics', '/v1/expenses/metrics', '/v1/expenses/_metrics']) {
  const res = await fetch(url + path, { redirect: 'manual' })
  const text = await res.text()
  check(`${path} serves no metrics (${res.status})`, !/^# (HELP|TYPE) /m.test(text) && !text.includes('backd_http_requests_total'), text.slice(0, 200))
}

console.log('\nThe stack\'s Prometheus:')
/** @param {string} query */
async function query(query) {
  const res = await fetch(`${prometheus}/api/v1/query?query=${encodeURIComponent(query)}`)
  const body = /** @type {any} */ (await res.json())
  return /** @type {any[]} */ (body.data?.result ?? [])
}
let up = []
for (let i = 0; i < 40; i++) {
  up = await query('up{job="backd"}')
  if (up.length >= 3 && up.every((r) => r.value[1] === '1')) break
  await new Promise((r) => setTimeout(r, 2000))
}
check('every process is scraped and up (api, executor, egress)', up.length >= 3 && up.every((r) => r.value[1] === '1'), up)

// Some traffic, so there is something in the labels to inspect.
await fetch(url + '/v1/expenses/_auth/me').catch(() => {})
await fetch(url + '/no/such/path/for-someone@example.com').catch(() => {})
let routes = []
for (let i = 0; i < 20; i++) {
  routes = await query('backd_http_requests_total')
  if (routes.some((r) => r.metric.route === 'unmatched')) break
  await new Promise((r) => setTimeout(r, 2000))
}
check('requests are counted by route pattern, unknown paths as one "unmatched"', routes.some((r) => r.metric.route === 'unmatched') && routes.some((r) => String(r.metric.route).includes('{realm}')), routes.map((r) => r.metric.route))
const all = JSON.stringify(await query('{__name__=~"backd_.+"}'))
check('no email address, path or token in any backd label', !/@|bd[a-z]_[A-Za-z0-9]|no\/such|someone/.test(all.replace(/"__name__":"[^"]*"/g, '')), all.slice(0, 300))

console.log('\nThe stack\'s Grafana:')
const grafana = process.env.GRAFANA_URL ?? 'http://localhost:3000'
/** @type {any[]} */ let found = []
for (let i = 0; i < 40; i++) {
  try {
    found = /** @type {any[]} */ (await (await fetch(`${grafana}/api/search?tag=backd`)).json())
    if (found.length >= 5) break
  } catch { /* still starting */ }
  await new Promise((r) => setTimeout(r, 2000))
}
check('the five dashboards are provisioned', found.length === 5, found.map((d) => d.title))
const health = /** @type {any} */ (await (await fetch(`${grafana}/api/datasources/uid/prometheus/health`)).json().catch(() => ({})))
check('the Prometheus data source works', health.status === 'OK', health)

console.log(unexpected === 0 ? '\nThe metrics are private and clean.' : `\n${unexpected} result(s) differ from what is expected.`)
process.exit(unexpected === 0 ? 0 : 1)
