package devreport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/cmdtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestProblemsOfAConfig(t *testing.T) {
	root := cmdtest.Write(t, map[string]string{
		"app/realm.yaml":                 "auth:\n  strategies:\n    password: [password]\n  signup: open\nbogus: 1\n",
		"app/main/notes/schema.json":     "{bad json",
		"app/main/tasks/schema.json":     "{}",
		"app/main/tasks/collection.yaml": "rules:\n  read: user ==\n",
	})
	_, err := registry.Load(root)
	if err == nil {
		t.Fatal("the config loaded")
	}
	got := Of(fmt.Errorf(ConfigPrefix+"%w", err), root)
	want := map[string]Problem{
		"app/realm.yaml":                 {Kind: Config, Line: 5},
		"app/main/notes/schema.json":     {Kind: Config},
		"app/main/tasks/collection.yaml": {Kind: Rules},
	}
	if len(got) != len(want) {
		t.Fatalf("problems = %+v", got)
	}
	for _, p := range got {
		w, ok := want[p.File]
		if !ok || p.Kind != w.Kind || p.Line != w.Line {
			t.Errorf("problem %+v, want %+v for %s", p, w, p.File)
		}
		if strings.Contains(p.Message, root) || !strings.Contains(p.Hint, p.File) {
			t.Errorf("message %q or hint %q", p.Message, p.Hint)
		}
	}
}

func TestProblemsOfABuild(t *testing.T) {
	out := "error: The module's source code could not be parsed: Expected ';', '}' or <eof> at file:///config/app/main/_functions/hello/index.ts:3:7\n\n    const x y = 1;\n          ~"
	err := BuildError{Err: fmt.Errorf("app/main: %w", fmt.Errorf("function hello: %w", errors.New("deno bundle failed:\n"+out+"\n")))}
	got := Of(err, "/somewhere/else")
	if len(got) != 1 {
		t.Fatalf("problems = %+v", got)
	}
	p := got[0]
	if p.Kind != Build || p.File != "app/main/_functions/hello/index.ts" || p.Line != 3 || p.Output != out ||
		p.Message != "app/main: function hello: deno bundle failed" || !strings.Contains(p.Hint, "builds it again") {
		t.Errorf("problem = %+v", p)
	}
	// Without a source line, the function's folder.
	err = BuildError{Err: errors.New("app/main: function hello: deno bundle failed:\nerror: something else")}
	if p := Of(err, "/x")[0]; p.File != "app/main/_functions/hello" || p.Line != 0 {
		t.Errorf("problem = %+v", p)
	}
}

func TestLogProblemsOncePerProblem(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	ps := []Problem{{Kind: Config, File: "a/realm.yaml", Line: 2, Message: "bad", Hint: "fix"}, {Kind: Build, Message: "b", Output: "deno says", Hint: "fix"}}
	seen := map[string]bool{}
	Log(log, "waiting", ps, seen)
	Log(log, "waiting", ps, seen)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("records: %s", buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["level"] != "ERROR" || rec["kind"] != "config" || rec["file"] != "a/realm.yaml" || rec["line"] != float64(2) || rec["error"] != "bad" || rec["hint"] != "fix" {
		t.Errorf("record = %v", rec)
	}
	if !strings.Contains(lines[1], `"output":"deno says"`) {
		t.Errorf("build record = %s", lines[1])
	}
}
