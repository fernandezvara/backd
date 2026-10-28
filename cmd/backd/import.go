package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
)

// importBeginAnswer is the answer of POST /_admin/import.
type importBeginAnswer struct {
	Lease         string              `json:"lease"`
	MaxChunkBytes int64               `json:"max_chunk_bytes"`
	Warnings      []string            `json:"warnings"`
	FileFields    map[string][]string `json:"file_fields"`
	// NeedsKeys are the encrypted fields of the package nobody here can open; KeysNeeded says
	// the target opens some itself and wants the package's wrapped keys.
	NeedsKeys []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"needs_keys"`
	KeysNeeded bool `json:"keys_needed"`
}

// importProblem is a document that did not pass, or clashed, as the server reports it.
type importProblem struct {
	Part     string `json:"part"`
	ID       string `json:"id"`
	Line     int    `json:"line"`
	Outcome  string `json:"outcome"`
	Problems []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"problems"`
}

type importChunkAnswer struct {
	Documents     int             `json:"documents"`
	Counts        map[string]int  `json:"counts"`
	Problems      []importProblem `json:"problems"`
	MissingOwners int             `json:"missing_owners"`
	MissingOrgs   int             `json:"missing_orgs"`
}

// importTotals is what a pass over the package came to.
type importTotals struct {
	counts                     map[string]int
	problems                   []importProblem
	missingOwners, missingOrgs int
}

const maxReportedProblems = 1000

func (t *importTotals) add(part string, base int, a importChunkAnswer) {
	for k, n := range a.Counts {
		t.counts[k] += n
	}
	t.missingOwners += a.MissingOwners
	t.missingOrgs += a.MissingOrgs
	for _, p := range a.Problems {
		if len(t.problems) < maxReportedProblems {
			p.Part = part
			p.Line += base
			t.problems = append(t.problems, p)
		}
	}
}

// importAction handles `backd import`: it reads a package, checks every file against the
// manifest's checksums, has the running backd validate all of its documents against the
// target realm's configuration, and only then uploads the files and writes the documents.
// Documents keep their ids and _meta, so running it again after an interruption finishes the
// job: what was written already is found identical and left alone.
func importAction(c *kli.CommandContext) error {
	mode := str(c, "mode")
	if mode != "create" && mode != "skip" && mode != "replace" {
		return usageErr(fmt.Errorf("--mode: want create, skip or replace, got %q", mode))
	}
	dry, skipInvalid := flag(c, "dry-run"), flag(c, "skip-invalid")
	stderr := c.Stderr()
	dir, cleanup, err := openPackage(str(c, "from"))
	if err != nil {
		return err
	}
	defer cleanup()
	m, err := verifyPackage(dir, stderr)
	if err != nil {
		return err
	}
	keys, err := parseFieldKeys(str(c, "field"), c.Getenv, stderr)
	if err != nil {
		return usageErr(err)
	}
	packKeys, err := packageKeys(dir)
	if err != nil {
		return err
	}
	dec, err := newDecryptor(m, keys, packKeys)
	if err != nil {
		return usageErr(err)
	}
	var ek *auth.SecretCipher
	if spec := str(c, "auth-key"); spec != "" {
		text, err := readKeySource(spec, c.Getenv, stderr)
		if err != nil {
			return usageErr(fmt.Errorf("--auth-key: %w", err))
		}
		if ek, err = auth.ExportKey(text); err != nil {
			return usageErr(fmt.Errorf("--auth-key: %w", err))
		}
		if m.AuthKey != "" && ek.KeyID != m.AuthKey {
			return fmt.Errorf("--auth-key is not the key this package was exported with (its fingerprint is %s, the package's is %s)", ek.KeyID, m.AuthKey)
		}
	}
	if m.Flags.Auth && ek == nil {
		fmt.Fprintln(stderr, "note: no --auth-key: users, identities, organizations and teams are imported, but without second factors, push devices and Apple tokens (users enroll again at their next sign-in)")
	}
	t, closeT, d, err := openTarget(c)
	if err != nil {
		return err
	}
	defer closeT()
	if d != nil && !dry {
		if err := d.refuseIfServed(context.Background(), flag(c, "force"), stderr); err != nil {
			return err
		}
	}
	proof, err := t.proof(ioOf(c))
	if err != nil {
		return err
	}
	slim := *m
	slim.Checksums = nil
	// The fields this command opens arrive in plaintext, and the manifest it sends says so.
	slim.Encrypted = append([]exportpkg.EncryptedField(nil), m.Encrypted...)
	for i := range slim.Encrypted {
		e := &slim.Encrypted[i]
		for _, opened := range dec.opened[e.Database+"/"+e.Collection] {
			if opened.Path == e.Path {
				e.Form = exportpkg.FormPlaintext
			}
		}
	}
	var begun importBeginAnswer
	body := map[string]any{"manifest": slim, "mode": mode, "skip_invalid": skipInvalid, "dry_run": dry}
	if _, err := t.callHeaders("POST", "_admin/import", nil, body, &begun, proof); err != nil {
		return err
	}
	lease := map[string]string{"X-Backd-Export": begun.Lease}
	complete := false
	defer func() {
		q := url.Values{}
		if complete {
			q.Set("complete", "true")
		}
		_, _ = t.callHeaders("DELETE", "_admin/import", q, nil, nil, lease)
	}()
	for _, w := range begun.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if len(begun.NeedsKeys) > 0 {
		fmt.Fprintln(stderr, "these encrypted fields need a key to be imported (--field <path>=<key:|env:|file:…>):")
		for _, n := range begun.NeedsKeys {
			fmt.Fprintf(stderr, "  %s: %s\n", n.Path, n.Reason)
		}
		complete = true
		return errors.New("the package can't be imported until those keys are given")
	}
	if begun.KeysNeeded {
		if err := sendDataKeys(t, dir, lease, begun.MaxChunkBytes); err != nil {
			return err
		}
	}
	if err := checkFileRefs(dir, m, begun.FileFields, stderr); err != nil {
		return err
	}

	fmt.Fprintln(stderr, "validating every document against the target's configuration…")
	val, err := runPhase(t, dir, m, "validate", lease, begun.MaxChunkBytes, ek, dec, stderr)
	if err != nil {
		return err
	}
	printImportReport(stderr, "validated", val, mode)
	blocking := val.counts["conflict"] > 0 || (val.counts["invalid"] > 0 && !skipInvalid)
	if path := str(c, "report"); path != "" || len(val.problems) > 0 && skipInvalid {
		if path == "" {
			path = "import-report.json"
		}
		if err := writeImportReport(path, val); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "problems written to %s\n", path)
	}
	if blocking {
		hint := ""
		if val.counts["invalid"] > 0 && val.counts["conflict"] == 0 {
			hint = ", or leave the invalid documents out with --skip-invalid"
		}
		return errors.New("nothing was written: fix the problems above" + hint)
	}
	if dry {
		complete = true
		fmt.Fprintln(stderr, "dry run: nothing was written")
		return nil
	}

	if err := uploadFiles(t, dir, m, begun.FileFields, lease, stderr); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "writing documents…")
	wr, err := runPhase(t, dir, m, "write", lease, begun.MaxChunkBytes, ek, dec, stderr)
	if err != nil {
		return err
	}
	printImportReport(stderr, "imported", wr, mode)
	if path := str(c, "report"); path != "" && len(wr.problems) > 0 {
		if err := writeImportReport(path, wr); err != nil {
			return err
		}
	}
	if wr.counts["conflict"] > 0 {
		return fmt.Errorf("%d documents clashed while writing (the target changed, or a unique value repeats inside the package): the rest was imported; fix them and run the import again", wr.counts["conflict"])
	}
	complete = true
	fmt.Fprintf(c.Stdout(), "imported %d documents (%d created, %d replaced, %d unchanged, %d skipped)\n",
		wr.counts["created"]+wr.counts["replaced"]+wr.counts["unchanged"]+wr.counts["skipped"], wr.counts["created"], wr.counts["replaced"], wr.counts["unchanged"], wr.counts["skipped"])
	return nil
}

func printImportReport(w io.Writer, verb string, t *importTotals, mode string) {
	fmt.Fprintf(w, "%s: %d would be created, %d replaced, %d unchanged, %d skipped (mode %s)\n", verb, t.counts["created"], t.counts["replaced"], t.counts["unchanged"], t.counts["skipped"], mode)
	if t.missingOwners > 0 || t.missingOrgs > 0 {
		fmt.Fprintf(w, "note: %d documents belong to users and %d to organizations the target doesn't have\n", t.missingOwners, t.missingOrgs)
	}
	if n := t.counts["invalid"]; n > 0 {
		fmt.Fprintf(w, "%d documents are invalid for the target:\n", n)
	}
	if n := t.counts["conflict"]; n > 0 {
		fmt.Fprintf(w, "%d documents conflict with what the target holds:\n", n)
	}
	shown := 0
	for _, p := range t.problems {
		if shown == 20 {
			fmt.Fprintf(w, "  … and more (see the report file)\n")
			break
		}
		shown++
		var reasons []string
		for _, d := range p.Problems {
			reasons = append(reasons, strings.TrimSpace(d.Path+" "+d.Reason))
		}
		fmt.Fprintf(w, "  %s line %d (%s): %s\n", p.Part, p.Line, p.ID, strings.Join(reasons, "; "))
	}
}

func writeImportReport(path string, t *importTotals) error {
	data, err := json.MarshalIndent(map[string]any{"counts": t.counts, "problems": t.problems, "missing_owners": t.missingOwners, "missing_orgs": t.missingOrgs}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// runPhase sends every collection of the package, in chunks, to one phase of the import.
func runPhase(t *target, dir string, m *exportpkg.Manifest, phase string, lease map[string]string, maxChunk int64, ek *auth.SecretCipher, dec *decryptor, stderr io.Writer) (*importTotals, error) {
	totals := &importTotals{counts: map[string]int{}}
	// Authentication data first, so the owners of the documents exist when they are checked.
	for _, ap := range m.Auth {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(ap.Part)))
		if err != nil {
			return nil, err
		}
		err = eachChunk(f, maxChunk, func(chunk []byte, firstLine int) error {
			chunk, err := prepareAuthChunk(ap.Name, chunk, ek)
			if err != nil {
				return fmt.Errorf("%s: %w", ap.Part, err)
			}
			if len(chunk) == 0 {
				return nil
			}
			var ans importChunkAnswer
			q := url.Values{"phase": {phase}, "part": {ap.Part}}
			if err := t.send("POST", "_admin/import", q, bytes.NewReader(chunk), int64(len(chunk)), "application/x-ndjson", lease, &ans); err != nil {
				return fmt.Errorf("%s: %w", ap.Part, err)
			}
			totals.add(ap.Part, firstLine-1, ans)
			return nil
		})
		f.Close()
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(stderr, "  %s done\n", ap.Part)
	}
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			f, err := os.Open(filepath.Join(dir, filepath.FromSlash(col.Part)))
			if err != nil {
				return nil, err
			}
			err = eachChunk(f, maxChunk, func(chunk []byte, firstLine int) error {
				chunk, err := dec.chunk(db.Name, col.Name, chunk)
				if err != nil {
					return fmt.Errorf("%s: %w", col.Part, err)
				}
				var ans importChunkAnswer
				q := url.Values{"phase": {phase}, "part": {col.Part}}
				if err := t.send("POST", "_admin/import", q, bytes.NewReader(chunk), int64(len(chunk)), "application/x-ndjson", lease, &ans); err != nil {
					return fmt.Errorf("%s: %w", col.Part, err)
				}
				totals.add(col.Part, firstLine-1, ans)
				return nil
			})
			f.Close()
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(stderr, "  %s/%s done\n", db.Name, col.Name)
		}
	}
	return totals, nil
}

// eachChunk reads JSON Lines and calls fn with groups of whole lines of up to max bytes (a
// longer line goes alone), with the number of the first line of the group.
func eachChunk(r io.Reader, max int64, fn func(chunk []byte, firstLine int) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	var buf bytes.Buffer
	first, line := 1, 0
	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		err := fn(buf.Bytes(), first)
		buf.Reset()
		return err
	}
	for {
		b, err := br.ReadBytes('\n')
		if len(b) > 0 {
			line++
			if buf.Len() > 0 && int64(buf.Len()+len(b)) > max {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			}
			if buf.Len() == 0 {
				first = line
			}
			buf.Write(b)
			if len(b) == 0 || b[len(b)-1] != '\n' {
				buf.WriteByte('\n')
			}
		}
		if errors.Is(err, io.EOF) {
			return flush()
		}
		if err != nil {
			return err
		}
	}
}

// checkFileRefs makes sure every file the documents reference is in the package.
func checkFileRefs(dir string, m *exportpkg.Manifest, fileFields map[string][]string, stderr io.Writer) error {
	var missing []string
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			fields := fileFields[db.Name+"/"+col.Name]
			if len(fields) == 0 {
				continue
			}
			ids, err := fileIDsOf(filepath.Join(dir, filepath.FromSlash(col.Part)), fields)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, ok := m.Checksums[exportpkg.FilePart(db.Name, id)]; !ok {
					missing = append(missing, db.Name+"/"+col.Name+": "+id)
				}
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if m.Flags.NoFiles {
		fmt.Fprintf(stderr, "warning: the package was made without files and %d files its documents reference won't exist in the target\n", len(missing))
		return nil
	}
	sort.Strings(missing)
	if len(missing) > 10 {
		missing = append(missing[:10], fmt.Sprintf("… and %d more", len(missing)-10))
	}
	return fmt.Errorf("documents reference files the package doesn't hold:\n  %s", strings.Join(missing, "\n  "))
}

// uploadFiles sends every file of the package to the target's storage, before the documents
// that reference them. A file already there is written again with the same bytes.
func uploadFiles(t *target, dir string, m *exportpkg.Manifest, fileFields map[string][]string, lease map[string]string, stderr io.Writer) error {
	n := 0
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			fields := fileFields[db.Name+"/"+col.Name]
			if len(fields) == 0 {
				continue
			}
			refs, err := fileRefsOf(filepath.Join(dir, filepath.FromSlash(col.Part)), fields)
			if err != nil {
				return err
			}
			for _, ref := range refs {
				id := ref.ID
				sum, ok := m.Checksums[exportpkg.FilePart(db.Name, id)]
				if !ok {
					continue // a package made without files
				}
				f, err := os.Open(filepath.Join(dir, filepath.FromSlash(exportpkg.FilePart(db.Name, id))))
				if err != nil {
					return err
				}
				st, _ := f.Stat()
				hdr := map[string]string{"X-Backd-Sha256": strings.TrimPrefix(sum, "sha256:")}
				maps.Copy(hdr, lease)
				query := url.Values{"file": {db.Name + "/" + col.Name + "/" + id}}
				if ref.Public {
					// Stored in the target's public storage, after the checks an upload does.
					query.Set("public", "true")
					query.Set("key_name", ref.KeyName)
				}
				err = t.send("PUT", "_admin/import", query, f, st.Size(), "application/octet-stream", hdr, nil)
				f.Close()
				if err != nil {
					return fmt.Errorf("file %s: %w", id, err)
				}
				n++
			}
		}
	}
	if n > 0 {
		fmt.Fprintf(stderr, "uploaded %d files\n", n)
	}
	return nil
}

// openPackage returns the directory of a package: the path itself, or an archive extracted
// to a private temporary directory that cleanup removes.
func openPackage(from string) (string, func(), error) {
	st, err := os.Stat(from)
	if err != nil {
		return "", nil, err
	}
	if st.IsDir() {
		return from, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "backd-import-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := extractArchive(from, tmp); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%s: %w", from, err)
	}
	// The archive holds one directory (what `backd export --archive` writes) or the files themselves.
	if _, err := os.Stat(filepath.Join(tmp, exportpkg.ManifestName)); err != nil {
		entries, _ := os.ReadDir(tmp)
		if len(entries) == 1 && entries[0].IsDir() {
			return filepath.Join(tmp, entries[0].Name()), cleanup, nil
		}
	}
	return tmp, cleanup, nil
}

// extractArchive unpacks a .tar.gz, refusing anything that isn't a regular file under a
// relative path without dot segments.
func extractArchive(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a .tar.gz: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || !exportpkg.SafePart(name) {
			return fmt.Errorf("the archive holds an entry that isn't a plain file in the package: %q", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}

// verifyPackage reads the manifest and checks every file it lists against its checksum.
func verifyPackage(dir string, stderr io.Writer) (*exportpkg.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, exportpkg.ManifestName))
	if err != nil {
		return nil, fmt.Errorf("this is not a backd export package: %w", err)
	}
	m, err := exportpkg.ParseManifest(data)
	if err != nil {
		return nil, err
	}
	var bad []string
	parts := make([]string, 0, len(m.Checksums))
	for p := range m.Checksums {
		parts = append(parts, p)
	}
	sort.Strings(parts)
	for _, p := range parts {
		if !exportpkg.SafePart(p) {
			return nil, fmt.Errorf("the manifest lists an unsafe path: %q", p)
		}
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			bad = append(bad, p+": missing")
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, err
		}
		if "sha256:"+hex.EncodeToString(h.Sum(nil)) != m.Checksums[p] {
			bad = append(bad, p+": checksum differs")
		}
	}
	// Every path the import will open comes from the manifest, so each must be the one this
	// package format names, and be checksummed: a hostile manifest can't point it elsewhere.
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			if !exportpkg.SafePart(db.Name) || !exportpkg.SafePart(col.Name) || col.Part != exportpkg.CollectionPart(db.Name, col.Name) {
				return nil, fmt.Errorf("the manifest names an unexpected part for %s/%s: %q", db.Name, col.Name, col.Part)
			}
			if _, ok := m.Checksums[col.Part]; !ok {
				bad = append(bad, col.Part+": not in the manifest's checksums")
			}
		}
	}
	for _, ap := range m.Auth {
		if !slices.Contains(auth.PortableCollections, ap.Name) || ap.Part != exportpkg.AuthPart(ap.Name) {
			return nil, fmt.Errorf("the manifest names an authentication part this backd doesn't know: %q", ap.Part)
		}
		if _, ok := m.Checksums[ap.Part]; !ok {
			bad = append(bad, ap.Part+": not in the manifest's checksums")
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("the package is damaged or was changed after it was made:\n  %s", strings.Join(bad, "\n  "))
	}
	fmt.Fprintf(stderr, "package checked: %d files match their checksums\n", len(parts))
	return m, nil
}

// prepareAuthChunk gets a chunk of authentication data ready for the server: with the export
// key, the fields sealed under it are opened here and sent as plaintext for the target to seal
// under its own key (the key never leaves this machine); without it, second factors and devices
// are left out and the sealed fields of the rest removed.
func prepareAuthChunk(collection string, chunk []byte, ek *auth.SecretCipher) ([]byte, error) {
	var out bytes.Buffer
	err := exportpkg.ReadDocuments(bytes.NewReader(chunk), func(_ int, doc map[string]any) error {
		if ek != nil {
			if err := auth.PortableToPlain(collection, doc, ek); err != nil {
				return err
			}
		} else if !auth.PortableWithoutSecrets(collection, doc) {
			return nil
		}
		line, err := exportpkg.EncodeDocument(doc)
		if err != nil {
			return err
		}
		out.Write(line)
		out.WriteByte('\n')
		return nil
	})
	return out.Bytes(), err
}

// sendDataKeys gives the server the package's wrapped keys, which it opens the ciphertext of
// fields it shares a key with.
func sendDataKeys(t *target, dir string, lease map[string]string, maxChunk int64) error {
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(exportpkg.DataKeysName)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return eachChunk(f, maxChunk, func(chunk []byte, _ int) error {
		var ans struct{}
		q := url.Values{"phase": {"keys"}, "part": {exportpkg.DataKeysName}}
		return t.send("POST", "_admin/import", q, bytes.NewReader(chunk), int64(len(chunk)), "application/x-ndjson", lease, &ans)
	})
}
