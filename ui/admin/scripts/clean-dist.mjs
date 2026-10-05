// Empties the embedded build directory but keeps .gitkeep, so a Go build
// without the UI still compiles (go:embed needs the directory to exist).
import { readdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = fileURLToPath(new URL('../../../internal/adminui/dist', import.meta.url))
for (const name of readdirSync(dist)) {
  if (name !== '.gitkeep') rmSync(join(dist, name), { recursive: true, force: true })
}
