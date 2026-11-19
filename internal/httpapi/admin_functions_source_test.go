package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// A function's source: its own files first, the shared modules it imports (however far), what
// it imports from outside the project, and whether the bundle is of these files.
func TestAdminFunctionSource(t *testing.T) {
	f := newRulesFixture(t)
	_, adminKey, err := f.svc.CreateAPIKey(t.Context(), "source-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := bearer(adminKey)
	dir := f.reg.Realms["acme"].Databases["app"].Functions.Dir
	url := admin + "/functions/app/echo/source"

	// As built: the bundle is of the files on disk.
	rec, got := f.doH(t, "GET", url, "", key)
	if rec.Code != http.StatusOK || got["function"] != "app/echo" || got["entry"] != "echo/index.js" {
		t.Fatalf("source: %d %v", rec.Code, got)
	}
	if b, _ := got["bundle"].(map[string]any); b == nil || b["current"] != true {
		t.Fatalf("bundle before any change: %v", got["bundle"])
	}

	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("echo/index.js", strings.Join([]string{
		`// import { nope } from "./commented.ts";`,
		`import { relay } from "../lib/relay.js";`,
		`import { z } from "zod";`,
		`import { hash } from "npm:object-hash@3.0.0/index.js";`,
		`import { readFile } from "node:fs";`,
		`import { serve } from "https://deno.land/std@0.200.0/http/server.ts";`,
		`import { defineFunction } from "@backd/functions";`,
		`import "../lib/missing.js";`,
		`import helper from "./helper.js";`,
		`export default defineFunction(() => relay());`,
	}, "\n"))
	write("echo/helper.js", `export default 1;`)
	write("echo/notes.bin", "ab\x00cd")
	write("lib/relay.js", `import { leaf } from "./leaf.js"; export const relay = () => leaf;`)
	write("lib/leaf.js", `export const leaf = 1;`)
	write("lib/unused.js", `export const unused = 1;`) // nothing imports it
	write("deno.json", `{ "imports": { "zod": "npm:zod@3.22.0", "@/": "./lib/" } }`)
	write("echo/.secret", "never read")
	write("echo/node_modules/dep/index.js", "never read")

	rec, got = f.doH(t, "GET", url, "", key)
	if rec.Code != http.StatusOK {
		t.Fatalf("source: %d %v", rec.Code, got)
	}
	var paths []string
	byPath := map[string]map[string]any{}
	for _, v := range got["files"].([]any) {
		file := v.(map[string]any)
		paths = append(paths, file["path"].(string))
		byPath[file["path"].(string)] = file
	}
	want := []string{"echo/index.js", "echo/function.yaml", "echo/helper.js", "echo/notes.bin", "deno.json", "lib/relay.js", "lib/leaf.js"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("files %v, want %v", paths, want)
	}
	if byPath["echo/index.js"]["role"] != "entry" || byPath["echo/function.yaml"]["role"] != "config" || byPath["lib/leaf.js"]["role"] != "shared" || byPath["deno.json"]["role"] != "project" || byPath["echo/helper.js"]["role"] != "code" {
		t.Errorf("roles: %v", byPath)
	}
	if byPath["echo/index.js"]["language"] != "javascript" || byPath["deno.json"]["language"] != "json" || byPath["echo/function.yaml"]["language"] != "yaml" {
		t.Errorf("languages: %v", byPath)
	}
	if c, _ := byPath["echo/helper.js"]["content"].(string); c != "export default 1;" {
		t.Errorf("helper content: %v", byPath["echo/helper.js"])
	}
	if bin := byPath["echo/notes.bin"]; bin["binary"] != true || bin["content"] != nil {
		t.Errorf("a binary file comes without content: %v", bin)
	}
	if b, _ := got["bundle"].(map[string]any); b == nil || b["current"] != false {
		t.Errorf("after editing the files the bundle is not current: %v", got["bundle"])
	}

	type dep struct{ kind, resolved, mapsTo, pkg, version string }
	deps := map[string]dep{}
	for _, v := range got["dependencies"].([]any) {
		d := v.(map[string]any)
		str := func(k string) string { s, _ := d[k].(string); return s }
		deps[str("specifier")] = dep{str("kind"), str("resolved"), str("maps_to"), str("package"), str("version")}
	}
	for spec, w := range map[string]dep{
		"../lib/relay.js":                {kind: "local", resolved: "lib/relay.js"},
		"./leaf.js":                      {kind: "local", resolved: "lib/leaf.js"},
		"./helper.js":                    {kind: "local", resolved: "echo/helper.js"},
		"../lib/missing.js":              {kind: "local"},
		"zod":                            {kind: "npm", mapsTo: "npm:zod@3.22.0", pkg: "zod", version: "3.22.0"},
		"npm:object-hash@3.0.0/index.js": {kind: "npm", pkg: "object-hash", version: "3.0.0"},
		"node:fs":                        {kind: "node"},
		"https://deno.land/std@0.200.0/http/server.ts": {kind: "url"},
		"@backd/functions": {kind: "backd"},
	} {
		if deps[spec] != w {
			t.Errorf("dependency %q = %+v, want %+v", spec, deps[spec], w)
		}
	}
	if _, ok := deps["./commented.ts"]; ok {
		t.Error("an import in a comment is not a dependency")
	}

	// Unknown functions, and the area's rights.
	if rec, _ := f.doH(t, "GET", admin+"/functions/app/ghost/source", "", key); rec.Code != http.StatusNotFound {
		t.Errorf("unknown function: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", admin+"/functions/nope/echo/source", "", key); rec.Code != http.StatusNotFound {
		t.Errorf("unknown database: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", url, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", url, "", bearer(f.ada)); rec.Code != http.StatusForbidden {
		t.Errorf("not an administrator: %d", rec.Code)
	}
}
