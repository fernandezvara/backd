#!/usr/bin/env bash
# Checks the JavaScript client the way a user gets it from npm: packs it,
# looks at what the tarball holds, installs the tarball into a fresh project and
# uses it from Node (ESM) and from TypeScript (the shipped declarations).
# Needs Node and npm; run `npm ci` in clients/js first (it builds types/ and
# provides tsc).
#
#   scripts/check-js-package.sh        # or: make js-package
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root/clients/js"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

npm pack --pack-destination "$work" --silent >/dev/null
tarball=$(ls "$work"/*.tgz)

# What the tarball holds: the library, its declarations, README and LICENSE;
# nothing of the tests, the examples or the sources of the declarations.
files=$(tar -tzf "$tarball" | sed 's#^package/##' | sort)
for want in package.json README.md LICENSE src/index.js types/index.d.ts; do
  grep -qx "$want" <<<"$files" || { echo "the package lacks $want" >&2; exit 1; }
done
if grep -Eq '^(test|examples|scripts|tsconfig|node_modules)' <<<"$files"; then
  echo "the package holds files it shouldn't:" >&2
  grep -E '^(test|examples|scripts|tsconfig|node_modules)' <<<"$files" >&2
  exit 1
fi
size=$(stat -c %s "$tarball")
echo "tarball: $(wc -l <<<"$files") files, $size bytes"

# A fresh project that installs it.
app="$work/app"
mkdir "$app"
cd "$app"
npm init -y >/dev/null
npm pkg set type=module >/dev/null
npm install --no-audit --no-fund --silent "$tarball"

cat > use.js <<'JS'
import { createClient, memoryStorage, BackdError, NotFoundError, VerificationRequiredError } from 'backd-js'

const calls = []
const client = createClient({
  url: 'https://api.example.com',
  realm: 'blog',
  storage: memoryStorage(),
  fetch: async (url, init) => {
    calls.push({ url: String(url), method: init?.method })
    return new Response(JSON.stringify({ items: [], next: null }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  },
})
const page = await client.db('main').collection('posts').list({ limit: 1 })
if (!Array.isArray(page.items) || calls.length !== 1 || !calls[0].url.startsWith('https://api.example.com/v1/blog/main/posts')) {
  throw new Error('unexpected call: ' + JSON.stringify(calls))
}
if (!(new NotFoundError('x') instanceof BackdError) || typeof VerificationRequiredError !== 'function') throw new Error('errors are missing')
console.log('node: ok')
JS
node use.js

# TypeScript with the strictest module resolution: the declarations resolve
# from the package's exports, and a wrong call doesn't compile.
cat > use.ts <<'TS'
import { createClient, type Client, type ClientOptions } from 'backd-js'

const options: ClientOptions = { url: 'https://api.example.com', realm: 'blog' }
const client: Client = createClient(options)
const posts = client.db('main').collection<{ title: string }>('posts')
const created: Promise<{ title: string }> = posts.create({ title: 'Hello' })
void created
// @ts-expect-error: a post's title is a string
void posts.create({ title: 42 })
TS
cat > tsconfig.json <<'JSON'
{ "compilerOptions": { "module": "nodenext", "moduleResolution": "nodenext", "target": "es2022", "lib": ["es2022", "dom"], "strict": true, "noEmit": true, "skipLibCheck": false, "types": [] }, "files": ["use.ts"] }
JSON
"$root/clients/js/node_modules/.bin/tsc" -p tsconfig.json
echo "typescript: ok"
