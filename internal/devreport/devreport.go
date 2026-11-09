// Package devreport is what dev mode (BACKD_DEV) says when a config doesn't load: the
// problems it holds, each with where it is, the tool's output and what to do, for the log
// and for the dev landing page and API answers.
package devreport

import (
	"errors"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Problem kinds: what failed when a config didn't load in dev mode.
const (
	Config = "config" // a file of CONFIG_DIR, or the config as a whole
	Rules  = "rules"  // a collection's rules
	Build  = "build"  // bundling a database's functions
)

// Problem is one reason a config didn't load, as dev mode reports it: in one log record
// and on the API root.
type Problem struct {
	Kind    string `json:"kind"`
	File    string `json:"file,omitempty"` // relative to CONFIG_DIR, when known
	Line    int    `json:"line,omitempty"` // when known
	Message string `json:"message"`
	Output  string `json:"output,omitempty"` // the tool's output as it is (Deno's, for a build)
	Hint    string `json:"hint"`
}

// BuildError marks an error of bundling the functions, so its report has kind build.
type BuildError struct{ Err error }

func (e BuildError) Error() string { return e.Err.Error() }
func (e BuildError) Unwrap() error { return e.Err }

// ConfigPrefix starts the error of a config that does not load.
const ConfigPrefix = "invalid config:\n"

var (
	yamlLine    = regexp.MustCompile(`\bline (\d+):`)
	buildPrefix = regexp.MustCompile(`^([a-z0-9_-]+)/([a-z0-9_-]+): function ([a-z0-9_-]+): `)
	sourceLine  = regexp.MustCompile(`(?:file://)?(/[^\s:]+\.(?:ts|tsx|js|jsx|mjs|mts)):(\d+)(?::\d+)?`)
)

// Of splits an error of loading the config into the problems it holds: one per file
// error of the config (errors.Join), and one for a build, with Deno's output.
func Of(err error, configDir string) []Problem {
	if err == nil {
		return nil
	}
	if be, ok := errors.AsType[BuildError](err); ok {
		return []Problem{buildProblem(be.Err.Error(), configDir)}
	}
	var out []Problem
	var walk func(error)
	walk = func(e error) {
		if multi, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range multi.Unwrap() {
				walk(inner)
			}
			return
		}
		if inner := errors.Unwrap(e); inner != nil && e.Error() == ConfigPrefix+inner.Error() {
			walk(inner)
			return
		}
		out = append(out, configProblem(strings.TrimPrefix(e.Error(), ConfigPrefix), configDir))
	}
	walk(err)
	return out
}

func configProblem(msg, configDir string) Problem {
	p := Problem{Kind: Config, Message: msg}
	if rel, rest, ok := underDir(msg, configDir); ok {
		p.File, p.Message = rel, rest
		if strings.HasPrefix(rest, "rules:") {
			p.Kind = Rules
		}
		if m := yamlLine.FindStringSubmatch(rest); m != nil {
			p.Line, _ = strconv.Atoi(m[1])
		}
	}
	switch {
	case p.Kind == Rules:
		p.Hint = "fix the rules in " + p.File + " and save: backd loads the config again"
	case p.File != "":
		p.Hint = "fix " + p.File + " and save: backd loads the config again"
	default:
		p.Hint = "fix the config or the setting named and save: backd loads the config again"
	}
	return p
}

// underDir reads "<path>: <rest>" for a path inside dir, returning the path relative to dir.
func underDir(msg, dir string) (rel, rest string, ok bool) {
	path, rest, found := strings.Cut(msg, ": ")
	if !found || dir == "" {
		return "", "", false
	}
	r, err := filepath.Rel(dir, path)
	if err != nil || r == "." || strings.HasPrefix(r, "..") || filepath.IsAbs(r) {
		return "", "", false
	}
	return filepath.ToSlash(r), rest, true
}

func buildProblem(msg, configDir string) Problem {
	p := Problem{Kind: Build, Message: msg}
	if head, output, ok := strings.Cut(msg, "deno bundle failed:\n"); ok {
		p.Message, p.Output = strings.TrimSuffix(head, ": ")+": deno bundle failed", strings.TrimRight(output, "\n")
	}
	if m := buildPrefix.FindStringSubmatch(msg); m != nil {
		p.File = m[1] + "/" + m[2] + "/_functions/" + m[3]
	}
	// Deno names the source file and line of a syntax or type error.
	if m := sourceLine.FindStringSubmatch(p.Output); m != nil {
		for _, dir := range []string{configDir, "/config"} {
			if rel, _, ok := underDir(m[1]+": x", dir); ok {
				p.File = rel
				p.Line, _ = strconv.Atoi(m[2])
				break
			}
		}
	}
	p.Hint = "fix the function and save: backd builds it again"
	if p.File != "" {
		p.Hint = "fix " + p.File + " and save: backd builds it again"
	}
	return p
}

// Key identifies a problem, so a report repeated without change is logged once.
func (p Problem) Key() string {
	return p.Kind + "\x00" + p.File + "\x00" + strconv.Itoa(p.Line) + "\x00" + p.Message + "\x00" + p.Output
}

// Log writes one error record per problem not in seen (and adds it there), with msg
// saying what dev mode does about it.
func Log(log *slog.Logger, msg string, problems []Problem, seen map[string]bool) {
	for _, p := range problems {
		if seen != nil {
			if seen[p.Key()] {
				continue
			}
			seen[p.Key()] = true
		}
		attrs := []any{"kind", p.Kind}
		if p.File != "" {
			attrs = append(attrs, "file", p.File)
		}
		if p.Line > 0 {
			attrs = append(attrs, "line", p.Line)
		}
		attrs = append(attrs, "error", p.Message)
		if p.Output != "" {
			attrs = append(attrs, "output", p.Output)
		}
		attrs = append(attrs, "hint", p.Hint)
		log.Error(msg, attrs...)
	}
}

// Location is where the problem is: "file:line", "file", or "" when it isn't in one file.
func (p Problem) Location() string {
	if p.File != "" && p.Line > 0 {
		return p.File + ":" + strconv.Itoa(p.Line)
	}
	return p.File
}

// String is the problem on one line: where, then what ("app/realm.yaml:2: …").
func (p Problem) String() string {
	if loc := p.Location(); loc != "" {
		return loc + ": " + p.Message
	}
	return p.Message
}
