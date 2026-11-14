package typegen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/strutil"
)

// MigrationsFile is the runtime module `backd types --lang ts` writes next to the types: the
// collections' own migrations, as TypeScript functions apps run with `upgrade`.
const MigrationsFile = "migrations.ts"

// importTypeRe is the one import a migration may have (the build refuses any other): `import type
// { … } from "./schema.d.ts";`, which migrations.ts replaces by the types written inline.
var importTypeRe = regexp.MustCompile(`(?s)import\s+type\s*\{[^}]*\}\s*from\s*["']\./schema\.d\.ts["']\s*;?[ \t]*\n?`)

var exportDefaultRe = regexp.MustCompile(`\bexport\s+default\s+`)

var leadingExportRe = regexp.MustCompile(`(?m)^export `)

const migrationsPrelude = `/*
 * The migrations of the collections that have them, as functions an app can run: the same code
 * the server runs in the executor, so the app and the server can't disagree. They are pure and
 * synchronous: the clock and randomness throw while one runs, as they do on the server.
 */

type Step = readonly [to: number, run: (doc: any, meta: any) => unknown];

const realDate = Date;
const refuse = (): never => {
  throw new Error("a migration must be deterministic: it can't read the clock or randomness");
};

// Runs fn with the clock and randomness unavailable, and puts them back.
function withoutNondeterminism<T>(fn: () => T): T {
  const saved = {
    Date: globalThis.Date,
    now: performance.now,
    random: Math.random,
    getRandomValues: globalThis.crypto?.getRandomValues,
    randomUUID: globalThis.crypto?.randomUUID,
  };
  function GuardedDate(this: unknown, ...args: unknown[]) {
    if (!new.target || args.length === 0) refuse();
    return Reflect.construct(realDate, args, realDate);
  }
  GuardedDate.prototype = realDate.prototype;
  Object.setPrototypeOf(GuardedDate, realDate);
  (GuardedDate as unknown as { now: () => never }).now = refuse;
  try {
    (globalThis as { Date: unknown }).Date = GuardedDate;
    performance.now = refuse;
    Math.random = refuse;
    if (globalThis.crypto) {
      (globalThis.crypto as { getRandomValues: unknown }).getRandomValues = refuse;
      (globalThis.crypto as { randomUUID: unknown }).randomUUID = refuse;
    }
    return fn();
  } finally {
    (globalThis as { Date: unknown }).Date = saved.Date;
    performance.now = saved.now;
    Math.random = saved.random;
    if (globalThis.crypto) {
      (globalThis.crypto as { getRandomValues: unknown }).getRandomValues = saved.getRandomValues;
      (globalThis.crypto as { randomUUID: unknown }).randomUUID = saved.randomUUID;
    }
  }
}

function deepFreeze<T>(v: T): T {
  if (v !== null && typeof v === "object" && !Object.isFrozen(v)) {
    Object.freeze(v);
    for (const k of Object.keys(v as object)) deepFreeze((v as Record<string, unknown>)[k]);
  }
  return v;
}

// Takes a document of any kept version to the current one, through the steps in order. A document
// already at the current version is returned as it is.
function upgradeTo(name: string, current: number, steps: readonly Step[], doc: any): any {
  const { id, _meta, ...rest } = doc;
  const from: number = Number.isInteger(_meta?.schema_version) && _meta.schema_version >= 1 ? _meta.schema_version : 1;
  if (from >= current) return doc;
  const meta = deepFreeze({ ..._meta, id, version: _meta?.version });
  let fields: Record<string, unknown> = rest;
  let version = from;
  for (const [to, run] of steps) {
    if (to <= version) continue;
    if (to !== version + 1) throw new Error(name + ": there is no migration from version " + version + " to " + (version + 1));
    const next = withoutNondeterminism(() => run(structuredClone(fields), meta));
    if (next === null || typeof next !== "object" || Array.isArray(next) || typeof (next as { then?: unknown }).then === "function") {
      throw new Error(name + ": the migration to version " + to + " must return an object, synchronously");
    }
    fields = JSON.parse(JSON.stringify(next)) as Record<string, unknown>;
    delete fields.id;
    delete fields._meta;
    version = to;
  }
  if (version !== current) throw new Error(name + ": there is no migration from version " + version + " to " + current);
  return { ...fields, id, _meta: { ..._meta, schema_version: current } };
}
`

// upgradeName makes the name of a collection's entry in upgrade: main/assets → mainAssets.
func upgradeName(db, name string) string {
	p := strutil.Pascal(db) + strutil.Pascal(name)
	return strings.ToLower(p[:1]) + p[1:]
}

// Migrations returns the contents of migrations.ts for the realm: one `upgrade.<collection>` per
// collection that has migration steps, built from its schema.d.ts types and its schema.vN.ts
// files. ok is false when the realm has none. The module imports the realm's types from
// "./types".
func Migrations(reg *registry.Registry, realm string) (content string, ok bool, err error) {
	rl, found := reg.Realms[realm]
	if !found {
		return "", false, fmt.Errorf("unknown realm %q", realm)
	}
	var blocks, entries, imports []string
	for _, dbName := range slices.Sorted(keys(rl.Databases)) {
		for _, c := range rl.Databases[dbName].SortedCollections() {
			if !c.Versioned || len(c.Migrations) == 0 {
				continue
			}
			types, _, _, err := SchemaTypes(c)
			if err != nil {
				return "", false, err
			}
			block, err := migrationBlock(c, types)
			if err != nil {
				return "", false, err
			}
			prefix := strutil.Pascal(dbName) + strutil.Pascal(c.Name)
			cam := upgradeName(dbName, c.Name)
			blocks = append(blocks, block)
			imports = append(imports, prefix, fmt.Sprintf("%sV%d", prefix, c.SchemaVersion))
			entries = append(entries, fmt.Sprintf("  /** Takes a %s/%s document of any kept version to version %d (the current one). */\n  %s(doc: %s): %sV%d {\n    return upgradeTo(%q, %d, %sSteps, doc);\n  },",
				dbName, c.Name, c.SchemaVersion, cam, prefix, prefix, c.SchemaVersion, dbName+"/"+c.Name, c.SchemaVersion, cam))
		}
	}
	if len(entries) == 0 {
		return "", false, nil
	}
	head := fmt.Sprintf("// Generated by `backd types --lang ts` (generator %s, realm %q). Do not edit: change the migrations and generate again.\n", Version, realm)
	head += fmt.Sprintf("import type { %s } from \"./types\";\n\n", strings.Join(unique(imports), ", "))
	return head + migrationsPrelude + "\n" + strings.Join(blocks, "\n") + "\nexport const upgrade = {\n" + strings.Join(entries, "\n") + "\n};\n", true, nil
}

// migrationBlock renders one collection's steps: its schema types (from schema.d.ts) and each
// schema.vN.ts, inside a function so the type names V1…VN, Meta and Current are local to it.
func migrationBlock(c *registry.Collection, types string) (string, error) {
	cam := upgradeName(c.Database, c.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "const %sSteps: readonly Step[] = (() => {\n", cam)
	// The types, without the exports that only a module level may have.
	for _, line := range strings.Split(strings.TrimRight(leadingExportRe.ReplaceAllString(types, ""), "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n")
	var steps []string
	for _, n := range c.Migrations {
		path := filepath.Join(c.Dir, registry.SchemaMigrationFile(n))
		src, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		code := importTypeRe.ReplaceAllString(string(src), "")
		if !exportDefaultRe.MatchString(code) {
			return "", fmt.Errorf("%s: a migration needs `export default` of its function", path)
		}
		code = exportDefaultRe.ReplaceAllString(code, fmt.Sprintf("const step%d = ", n))
		fmt.Fprintf(&b, "  // %s\n", registry.SchemaMigrationFile(n))
		for _, line := range strings.Split(strings.TrimRight(code, "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("  ;\n\n")
		steps = append(steps, fmt.Sprintf("[%d, step%d]", n, n))
	}
	fmt.Fprintf(&b, "  return [%s] as const;\n})();\n", strings.Join(steps, ", "))
	return b.String(), nil
}
