package admin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/authcore"
	"github.com/fernandezvara/backd/internal/cmdutil"
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
func importAction(c *kli.CommandContext, o Opener) error {
	mode := cmdutil.Str(c, "mode")
	if mode != "create" && mode != "skip" && mode != "replace" {
		return cmdutil.UsageErr(fmt.Errorf("--mode: want create, skip or replace, got %q", mode))
	}
	dry, skipInvalid := cmdutil.Flag(c, "dry-run"), cmdutil.Flag(c, "skip-invalid")
	stderr := c.Stderr()
	dir, cleanup, err := clicore.OpenPackage(cmdutil.Str(c, "from"))
	if err != nil {
		return err
	}
	defer cleanup()
	m, err := clicore.VerifyPackage(dir, stderr)
	if err != nil {
		return err
	}
	keys, err := clicore.ParseFieldKeys(cmdutil.Str(c, "field"), c.Getenv, stderr)
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	packKeys, err := clicore.PackageKeys(dir)
	if err != nil {
		return err
	}
	dec, err := clicore.NewDecryptor(m, keys, packKeys)
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	var ek *authcore.SecretCipher
	if spec := cmdutil.Str(c, "auth-key"); spec != "" {
		text, err := cmdutil.ReadKeySource(spec, c.Getenv, stderr)
		if err != nil {
			return cmdutil.UsageErr(fmt.Errorf("--auth-key: %w", err))
		}
		if ek, err = authcore.ExportKey(text); err != nil {
			return cmdutil.UsageErr(fmt.Errorf("--auth-key: %w", err))
		}
		if m.AuthKey != "" && ek.KeyID != m.AuthKey {
			return fmt.Errorf("--auth-key is not the key this package was exported with (its fingerprint is %s, the package's is %s)", ek.KeyID, m.AuthKey)
		}
	}
	if m.Flags.Auth && ek == nil {
		fmt.Fprintln(stderr, "note: no --auth-key: users, identities, organizations and teams are imported, but without second factors, push devices and Apple tokens (users enroll again at their next sign-in)")
	}
	s, err := o.Open(c)
	if err != nil {
		return err
	}
	defer s.Close()
	t := s.t
	if s.BeforeWrite != nil && !dry {
		if err := s.BeforeWrite(c); err != nil {
			return err
		}
	}
	proof, err := t.Proof(cmdutil.IOOf(c))
	if err != nil {
		return err
	}
	slim := *m
	slim.Checksums = nil
	// The fields this command opens arrive in plaintext, and the manifest it sends says so.
	slim.Encrypted = append([]exportpkg.EncryptedField(nil), m.Encrypted...)
	for i := range slim.Encrypted {
		e := &slim.Encrypted[i]
		for _, opened := range dec.Opened[e.Database+"/"+e.Collection] {
			if opened.Path == e.Path {
				e.Form = exportpkg.FormPlaintext
			}
		}
	}
	var begun importBeginAnswer
	body := map[string]any{"manifest": slim, "mode": mode, "skip_invalid": skipInvalid, "dry_run": dry}
	if err := t.Adm.Import.Begin(t.Ctx, body, &begun, clicore.WithProof(proof)...); err != nil {
		return err
	}
	lease := map[string]string{"X-Backd-Export": begun.Lease}
	complete := false
	defer func() {
		q := url.Values{}
		if complete {
			q.Set("complete", "true")
		}
		_ = t.Adm.Import.Abort(t.Ctx, q, clicore.WithHeaders(lease)...)
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
	if path := cmdutil.Str(c, "report"); path != "" || len(val.problems) > 0 && skipInvalid {
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
	if path := cmdutil.Str(c, "report"); path != "" && len(wr.problems) > 0 {
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
func runPhase(t *clicore.Target, dir string, m *exportpkg.Manifest, phase string, lease map[string]string, maxChunk int64, ek *authcore.SecretCipher, dec *clicore.Decryptor, stderr io.Writer) (*importTotals, error) {
	totals := &importTotals{counts: map[string]int{}}
	// Authentication data first, so the owners of the documents exist when they are checked.
	for _, ap := range m.Auth {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(ap.Part)))
		if err != nil {
			return nil, err
		}
		err = EachChunk(f, maxChunk, func(chunk []byte, firstLine int) error {
			chunk, err := prepareAuthChunk(ap.Name, chunk, ek)
			if err != nil {
				return fmt.Errorf("%s: %w", ap.Part, err)
			}
			if len(chunk) == 0 {
				return nil
			}
			var ans importChunkAnswer
			q := url.Values{"phase": {phase}, "part": {ap.Part}}
			if err := t.AdmLong.Import.Send(t.Ctx, "POST", q, bytes.NewReader(chunk), "application/x-ndjson", &ans, clicore.WithHeaders(lease)...); err != nil {
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
			err = EachChunk(f, maxChunk, func(chunk []byte, firstLine int) error {
				chunk, err := dec.Chunk(db.Name, col.Name, chunk)
				if err != nil {
					return fmt.Errorf("%s: %w", col.Part, err)
				}
				var ans importChunkAnswer
				q := url.Values{"phase": {phase}, "part": {col.Part}}
				if err := t.AdmLong.Import.Send(t.Ctx, "POST", q, bytes.NewReader(chunk), "application/x-ndjson", &ans, clicore.WithHeaders(lease)...); err != nil {
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
func EachChunk(r io.Reader, max int64, fn func(chunk []byte, firstLine int) error) error {
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
			ids, err := FileIDsOf(filepath.Join(dir, filepath.FromSlash(col.Part)), fields)
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
func uploadFiles(t *clicore.Target, dir string, m *exportpkg.Manifest, fileFields map[string][]string, lease map[string]string, stderr io.Writer) error {
	n := 0
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			fields := fileFields[db.Name+"/"+col.Name]
			if len(fields) == 0 {
				continue
			}
			refs, err := FileRefsOf(filepath.Join(dir, filepath.FromSlash(col.Part)), fields)
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
				err = t.AdmLong.Import.Send(t.Ctx, "PUT", query, io.NewSectionReader(f, 0, st.Size()), "application/octet-stream", nil, clicore.WithHeaders(hdr)...)
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

// prepareAuthChunk gets a chunk of authentication data ready for the server: with the export
// key, the fields sealed under it are opened here and sent as plaintext for the target to seal
// under its own key (the key never leaves this machine); without it, second factors and devices
// are left out and the sealed fields of the rest removed.
func prepareAuthChunk(collection string, chunk []byte, ek *authcore.SecretCipher) ([]byte, error) {
	var out bytes.Buffer
	err := exportpkg.ReadDocuments(bytes.NewReader(chunk), func(_ int, doc map[string]any) error {
		if ek != nil {
			if err := authcore.PortableToPlain(collection, doc, ek); err != nil {
				return err
			}
		} else if !authcore.PortableWithoutSecrets(collection, doc) {
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
func sendDataKeys(t *clicore.Target, dir string, lease map[string]string, maxChunk int64) error {
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(exportpkg.DataKeysName)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return EachChunk(f, maxChunk, func(chunk []byte, _ int) error {
		var ans struct{}
		q := url.Values{"phase": {"keys"}, "part": {exportpkg.DataKeysName}}
		return t.AdmLong.Import.Send(t.Ctx, "POST", q, bytes.NewReader(chunk), "application/x-ndjson", &ans, clicore.WithHeaders(lease)...)
	})
}
