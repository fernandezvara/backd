// Package functions builds server-side functions: `backd functions build`
// bundles each function of a database's Deno project into one file with
// `deno bundle`, and records the bundles in a manifest that startup checks.
package functions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// Options configure a build.
type Options struct {
	Deno  string // the deno binary; default "deno" on the PATH
	Check bool   // type-check TypeScript while bundling
	// Log receives one line per bundled function.
	Log func(format string, args ...any)
}

var versionPattern = regexp.MustCompile(`(?m)^deno (\d+\.\d+\.\d+)`)

// DenoVersion returns the version of the deno binary.
func DenoVersion(ctx context.Context, deno string) (string, error) {
	out, err := exec.CommandContext(ctx, deno, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("can't run %s: %w (install Deno %s, or run the build in the docker.io/denoland/deno:%s image)", deno, err, registry.DenoVersion, registry.DenoVersion)
	}
	m := versionPattern.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("%s --version: unexpected output %q", deno, out)
	}
	return string(m[1]), nil
}

// Build bundles every function of every database in reg that has any.
func Build(ctx context.Context, reg *registry.Registry, opts Options) error {
	if opts.Deno == "" {
		opts.Deno = "deno"
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	var projects []*registry.Database
	for _, db := range reg.Databases() {
		if db.Functions != nil && len(db.Functions.Functions) > 0 {
			projects = append(projects, db)
		}
	}
	if len(projects) == 0 {
		return nil
	}
	v, err := DenoVersion(ctx, opts.Deno)
	if err != nil {
		return err
	}
	if v != registry.DenoVersion {
		return fmt.Errorf("this backd bundles functions with Deno %s, but %s is %s", registry.DenoVersion, opts.Deno, v)
	}
	for _, db := range projects {
		if err := buildProject(ctx, db, opts); err != nil {
			return fmt.Errorf("%s/%s: %w", db.Realm, db.Name, err)
		}
	}
	return nil
}

// Watch rebuilds each functions project's bundles whenever its sources
// change, until ctx is done. For `BACKD_DEV=true`: function code is picked
// up automatically; schemas, rules and realm.yaml still need a restart.
// A build failure (e.g. a syntax error) is logged and retried every tick
// until the source changes again or the problem is fixed.
func Watch(ctx context.Context, reg *registry.Registry, opts Options, interval time.Duration) {
	if opts.Deno == "" {
		opts.Deno = "deno"
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	var projects []*registry.Database
	last := map[string]string{}
	for _, db := range reg.Databases() {
		if db.Functions == nil || len(db.Functions.Functions) == 0 {
			continue
		}
		projects = append(projects, db)
		if m, err := registry.ReadManifest(db.Functions.Dir); err == nil {
			last[db.Functions.Dir] = m.Source
		}
	}
	if len(projects) == 0 {
		return
	}
	if v, err := DenoVersion(ctx, opts.Deno); err != nil {
		opts.Log("dev mode: %v", err)
		return
	} else if v != registry.DenoVersion {
		opts.Log("dev mode: this backd bundles functions with Deno %s, but %s is %s", registry.DenoVersion, opts.Deno, v)
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for _, db := range projects {
			hash, err := registry.SourceHash(db.Functions.Dir)
			if err != nil || hash == last[db.Functions.Dir] {
				continue
			}
			if err := buildProject(ctx, db, opts); err != nil {
				opts.Log("dev mode: rebuilding %s/%s failed: %v", db.Realm, db.Name, err)
				continue
			}
			last[db.Functions.Dir] = hash
			if after, err := registry.SourceHash(db.Functions.Dir); err == nil {
				last[db.Functions.Dir] = after // the build may have written deno.lock
			}
		}
	}
}

// buildProject bundles one database's functions into _functions/.build,
// replacing the previous build only when every function bundled.
func buildProject(ctx context.Context, db *registry.Database, opts Options) error {
	fns := db.Functions
	source, err := registry.SourceHash(fns.Dir)
	if err != nil {
		return err
	}
	buildDir := filepath.Join(fns.Dir, registry.BuildDir)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	m := registry.Manifest{Deno: registry.DenoVersion, Source: source, Functions: map[string]registry.ManifestBundle{}}
	names := make([]string, 0, len(fns.Functions))
	for name := range fns.Functions {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b, err := bundle(ctx, fns.Dir, fns.Functions[name], opts)
		if err != nil {
			return fmt.Errorf("function %s: %w", name, err)
		}
		sum := sha256.Sum256(b)
		hash := hex.EncodeToString(sum[:])
		file := name + "-" + hash[:16] + ".js"
		if err := os.WriteFile(filepath.Join(buildDir, file), b, 0o644); err != nil {
			return err
		}
		m.Functions[name] = registry.ManifestBundle{Bundle: file, SHA256: hash}
		opts.Log("bundled %s/%s/%s → %s (%d bytes)", db.Realm, db.Name, name, file, len(b))
	}
	// Bundling can write files into the project (deno.lock, the first time a function imports
	// a package): the manifest records the sources as they are now, or the next start would
	// find them changed since the build.
	if m.Source, err = registry.SourceHash(fns.Dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(buildDir, registry.ManifestFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(buildDir, registry.ManifestFile)); err != nil {
		return err
	}
	return prune(buildDir, m)
}

// bundle runs `deno bundle` for one function in the project directory, so
// its deno.json and deno.lock apply.
func bundle(ctx context.Context, dir string, fn *registry.Function, opts Options) ([]byte, error) {
	out, err := os.CreateTemp("", "backd-bundle-*.js")
	if err != nil {
		return nil, err
	}
	out.Close()
	defer os.Remove(out.Name())
	args := []string{"bundle", "--platform=deno", "--quiet", "--output", out.Name()}
	if _, err := os.Stat(filepath.Join(dir, "deno.lock")); err == nil {
		args = append(args, "--frozen-lockfile")
	}
	if opts.Check {
		args = append(args, "--check")
	}
	args = append(args, "./"+fn.Entry)
	cmd := exec.CommandContext(ctx, opts.Deno, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "DENO_NO_UPDATE_CHECK=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New("deno bundle failed:\n" + msg)
	}
	return os.ReadFile(out.Name())
}

// prune removes bundles the manifest doesn't list.
func prune(buildDir string, m registry.Manifest) error {
	keep := map[string]bool{registry.ManifestFile: true}
	for _, b := range m.Functions {
		keep[b.Bundle] = true
	}
	entries, err := os.ReadDir(buildDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && !keep[e.Name()] {
			if err := os.Remove(filepath.Join(buildDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
