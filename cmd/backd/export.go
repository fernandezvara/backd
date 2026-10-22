package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/exportpkg"
)

// progressName is the file an interrupted export leaves in its directory: the lease and the
// parts already complete, with their checksums, so running the command again continues.
const progressName = ".export-progress.json"

type exportProgress struct {
	Lease string                  `json:"lease"`
	Parts map[string]exportedPart `json:"parts"`
}

type exportedPart struct {
	Checksum  string `json:"checksum"`
	Size      int64  `json:"size"`
	Documents int64  `json:"documents,omitempty"`
}

// exportPlan is the answer of GET /_admin/export.
type exportPlan struct {
	Lease      string              `json:"lease"`
	Manifest   *exportpkg.Manifest `json:"manifest"`
	FileFields map[string][]string `json:"file_fields"`
	ExportKey  string              `json:"export_key"`
}

// exportAction handles `backd export`: it takes the realm's export from the running backd part
// by part into a package directory (or archive), verifying and recording each part so an
// interrupted run continues where it stopped.
func exportAction(c *kli.CommandContext) error {
	out := strings.TrimRight(str(c, "out"), "/")
	archive := flag(c, "archive")
	database, collections := str(c, "db"), splitList(str(c, "collection"))
	if len(collections) > 0 && database == "" {
		return usageErr(errors.New("--collection needs --db"))
	}
	format, fields, where := str(c, "format"), splitList(str(c, "fields")), str(c, "where")
	if format == "" {
		format = "jsonl"
	}
	if format != "jsonl" && format != "csv" {
		return usageErr(fmt.Errorf("--format: want jsonl or csv, got %q", format))
	}
	if where != "" && (database == "" || len(collections) != 1) {
		return usageErr(errors.New("--where needs --db and exactly one --collection"))
	}
	csvOut := format == "csv"
	include := str(c, "include")
	if include != "" && include != "auth" {
		return usageErr(fmt.Errorf("--include: want auth, got %q", include))
	}
	if include == "auth" && (database != "" || len(fields) > 0 || csvOut) {
		return usageErr(errors.New("--include auth exports the whole realm: not with --db, --collection, --fields, --where or --format csv"))
	}
	if keyOut := str(c, "auth-key-out"); keyOut != "" && include != "auth" {
		return usageErr(errors.New("--auth-key-out needs --include auth"))
	}
	if csvOut && flag(c, "no-files") {
		return usageErr(errors.New("--no-files has no meaning with --format csv: a CSV extract never holds files"))
	}
	anonymize := flag(c, "anonymize")
	var planBody map[string]any
	if anonymize {
		planBody = map[string]any{"anonymize": true}
		if v := str(c, "anonymize-files"); v != "" {
			if v != "keep" {
				return usageErr(fmt.Errorf("--anonymize-files: want keep, got %q", v))
			}
			planBody["anonymize_files"] = v
		}
		if path := str(c, "anonymize-config"); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return usageErr(fmt.Errorf("--anonymize-config: %w", err))
			}
			planBody["anonymize_config"] = string(data)
		}
		if spec := str(c, "staging-password"); spec != "" {
			if include != "auth" {
				return usageErr(errors.New("--staging-password needs --include auth: without users there is no password to set"))
			}
			pw, err := readKeySource(spec, c.Getenv, c.Stderr())
			if err != nil {
				return usageErr(fmt.Errorf("--staging-password: %w", err))
			}
			planBody["staging_password"] = pw
		}
		if csvOut {
			return usageErr(errors.New("--anonymize writes a package: not with --format csv"))
		}
	} else {
		for _, f := range []string{"anonymize-config", "staging-password", "anonymize-files"} {
			if str(c, f) != "" {
				return usageErr(fmt.Errorf("--%s needs --anonymize", f))
			}
		}
	}
	t, closeT, _, err := openTarget(c)
	if err != nil {
		return err
	}
	defer closeT()
	if archive {
		if _, err := os.Stat(out + ".tar.gz"); err == nil {
			return fmt.Errorf("%s.tar.gz exists: choose another --out or remove it", out)
		}
	}
	progress, err := prepareOut(out)
	if err != nil {
		return err
	}
	proof, err := t.proof(ioOf(c))
	if err != nil {
		return err
	}
	q := url.Values{}
	if database != "" {
		q.Set("database", database)
	}
	for _, name := range collections {
		q.Add("collection", name)
	}
	if flag(c, "no-files") {
		q.Set("files", "false")
	}
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}
	if where != "" {
		q.Set("where", where)
	}
	if csvOut {
		q.Set("format", "csv")
	}
	if include != "" {
		q.Set("include", include)
	}
	headers := map[string]string{}
	for k, v := range proof {
		headers[k] = v
	}
	if progress.Lease != "" {
		headers["X-Backd-Export"] = progress.Lease
	}
	var plan exportPlan
	method := "GET"
	var requestBody any
	if planBody != nil {
		method, requestBody = "POST", planBody
	}
	if _, err := t.callHeaders(method, "_admin/export", q, requestBody, &plan, headers); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == "export_running" {
			return fmt.Errorf("%w\nonly one export of a realm runs at a time", err)
		}
		return err
	}
	progress.Lease = plan.Lease
	if err := saveProgress(out, progress); err != nil {
		return err
	}
	lease := map[string]string{"X-Backd-Export": plan.Lease}
	stderr := c.Stderr()
	m := plan.Manifest
	m.Checksums = map[string]string{}
	fileIDs := map[string][]string{} // database -> file ids, in order
	for di := range m.Databases {
		db := &m.Databases[di]
		for ci := range db.Collections {
			col := &db.Collections[ci]
			key := db.Name + "/" + col.Name
			remotePart := col.Part
			if csvOut {
				col.Part = strings.TrimSuffix(col.Part, ".jsonl") + ".csv"
			}
			part, ok := progress.Parts[col.Part]
			if ok && partIntact(out, col.Part, part) {
				fmt.Fprintf(stderr, "%s: already exported (%d documents)\n", key, part.Documents)
			} else {
				fmt.Fprintf(stderr, "%s: exporting…\n", key)
				if csvOut {
					part, err = fetchCSV(t, out, col.Part, url.Values{"part": {remotePart}}, lease, fields)
				} else {
					part, err = fetchPart(t, out, col.Part, url.Values{"part": {remotePart}}, lease, true)
				}
				if err != nil {
					return err
				}
				progress.Parts[col.Part] = part
				if err := saveProgress(out, progress); err != nil {
					return err
				}
			}
			col.Documents = part.Documents
			m.Checksums[col.Part] = part.Checksum
			if fields := plan.FileFields[key]; len(fields) > 0 {
				ids, err := fileIDsOf(filepath.Join(out, filepath.FromSlash(col.Part)), fields)
				if err != nil {
					return err
				}
				for _, id := range ids {
					fileIDs[db.Name] = append(fileIDs[db.Name], col.Name+"/"+id)
				}
			}
		}
	}
	// Authentication data: fetched whole every time (never resumed), because the export key that
	// seals its secrets belongs to this run alone.
	for i := range m.Auth {
		part := m.Auth[i].Part
		fmt.Fprintf(stderr, "%s: exporting…\n", part)
		got, err := fetchPart(t, out, part, url.Values{"part": {part}}, lease, true)
		if err != nil {
			return err
		}
		m.Auth[i].Documents = got.Documents
		m.Checksums[part] = got.Checksum
	}
	if !csvOut {
		// The wrapped keys of encrypted fields; empty until a realm has encrypted fields.
		if err := os.MkdirAll(filepath.Join(out, "_system"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, filepath.FromSlash(exportpkg.DataKeysName)), nil, 0o600); err != nil {
			return err
		}
		m.Checksums[exportpkg.DataKeysName] = exportpkg.NewHasher().Sum()
	}
	for _, db := range m.Databases {
		for _, ref := range fileIDs[db.Name] {
			collection, id, _ := strings.Cut(ref, "/")
			part := exportpkg.FilePart(db.Name, id)
			if done, ok := progress.Parts[part]; ok && partIntact(out, part, done) {
				m.Checksums[part] = done.Checksum
				m.Files++
				continue
			}
			fmt.Fprintf(stderr, "%s/%s: file %s…\n", db.Name, collection, id)
			done, err := fetchPart(t, out, part, url.Values{"file": {db.Name + "/" + collection + "/" + id}}, lease, false)
			if err != nil {
				return err
			}
			progress.Parts[part] = done
			if err := saveProgress(out, progress); err != nil {
				return err
			}
			m.Checksums[part] = done.Checksum
			m.Files++
		}
	}
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, exportpkg.ManifestName), data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(out, progressName))
	var totals struct {
		AuthUnavailable int64 `json:"auth_unavailable"`
	}
	if _, err := t.callHeaders("DELETE", "_admin/export", url.Values{"complete": {"true"}}, nil, &totals, lease); err != nil {
		fmt.Fprintf(stderr, "the package is complete, but the server wasn't told: %v\n", err)
	}
	if totals.AuthUnavailable > 0 {
		fmt.Fprintf(stderr, "warning: %d second factors or push devices were left out because the realm's secrets key could not open them (with --direct, pass --master-key)\n", totals.AuthUnavailable)
	}
	if plan.ExportKey != "" {
		if keyOut := str(c, "auth-key-out"); keyOut != "" {
			if err := os.WriteFile(keyOut, []byte(plan.ExportKey+"\n"), 0o600); err != nil {
				return fmt.Errorf("the package is complete, but its export key could not be written (%v): export again", err)
			}
			fmt.Fprintf(stderr, "the export key was written to %s\n", keyOut)
		} else {
			fmt.Fprintf(stderr, "\nEXPORT KEY (shown once; backd keeps no copy): %s\nThe package holds password hashes. Second factors and push devices in it are sealed under this key: import with\n--auth-key to carry them, or users arrive without them. Keep it apart from the package.\n\n", plan.ExportKey)
		}
	}
	result := out
	if archive {
		if err := archiveDir(out, out+".tar.gz"); err != nil {
			return err
		}
		if err := os.RemoveAll(out); err != nil {
			return err
		}
		result = out + ".tar.gz"
	}
	var docs int64
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			docs += col.Documents
		}
	}
	fmt.Fprintf(stderr, "exported %d documents and %d files to %s\nthe package holds data of the realm: keep it where only the people who may read the realm can, and delete it when you are done\n", docs, m.Files, result)
	fmt.Fprintln(c.Stdout(), result)
	return nil
}

// fetchCSV downloads a collection's documents and writes them as a CSV table at part. The
// documents wait in a temporary file because the columns are known only after the last one.
func fetchCSV(t *target, out, part string, q url.Values, headers map[string]string, fields []string) (exportedPart, error) {
	tmp := strings.TrimSuffix(part, ".csv") + ".jsonl.tmp"
	got, err := fetchPart(t, out, tmp, q, headers, true)
	if err != nil {
		return exportedPart{}, err
	}
	tmpPath := filepath.Join(out, filepath.FromSlash(tmp))
	defer func() { _ = os.Remove(tmpPath) }()
	src, err := os.Open(tmpPath)
	if err != nil {
		return exportedPart{}, err
	}
	defer src.Close()
	dst := filepath.Join(out, filepath.FromSlash(part))
	f, err := os.OpenFile(dst+".partial", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return exportedPart{}, err
	}
	h := exportpkg.NewHasher()
	_, err = exportpkg.WriteCSV(src, io.MultiWriter(f, h), fields)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(dst+".partial", dst)
	}
	if err != nil {
		_ = os.Remove(dst + ".partial")
		return exportedPart{}, fmt.Errorf("%s: %w", part, err)
	}
	return exportedPart{Checksum: h.Sum(), Size: h.Size(), Documents: got.Documents}, nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// prepareOut creates the package directory, or opens the progress of an interrupted export in
// it. A directory with other content is refused: an export never mixes with existing files.
func prepareOut(out string) (*exportProgress, error) {
	p := &exportProgress{Parts: map[string]exportedPart{}}
	entries, err := os.ReadDir(out)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return p, os.MkdirAll(out, 0o700)
	case err != nil:
		return nil, err
	case len(entries) == 0:
		return p, nil
	}
	data, err := os.ReadFile(filepath.Join(out, progressName))
	if err != nil {
		return nil, fmt.Errorf("%s is not empty and holds no interrupted export: choose another --out", out)
	}
	if err := json.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(out, progressName), err)
	}
	if p.Parts == nil {
		p.Parts = map[string]exportedPart{}
	}
	return p, nil
}

func saveProgress(out string, p *exportProgress) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, progressName), data, 0o600)
}

// partIntact reports whether a part recorded as complete is on disk with its size, so a
// resumed export doesn't trust a file someone truncated.
func partIntact(out, part string, p exportedPart) bool {
	st, err := os.Stat(filepath.Join(out, filepath.FromSlash(part)))
	return err == nil && st.Size() == p.Size
}

// fetchPart downloads one part into the package, hashing it as it arrives; the file only
// appears under its name once whole. A transfer that breaks is retried from its start.
func fetchPart(t *target, out, part string, q url.Values, headers map[string]string, lines bool) (exportedPart, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		p, err := fetchPartOnce(t, out, part, q, headers, lines)
		if err == nil {
			return p, nil
		}
		var ae *apiError
		if errors.As(err, &ae) {
			return exportedPart{}, err
		}
		last = err
	}
	return exportedPart{}, fmt.Errorf("%s: %w", part, last)
}

func fetchPartOnce(t *target, out, part string, q url.Values, headers map[string]string, lines bool) (exportedPart, error) {
	res, err := t.open("_admin/export", q, headers)
	if err != nil {
		return exportedPart{}, err
	}
	defer res.Body.Close()
	dst := filepath.Join(out, filepath.FromSlash(part))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return exportedPart{}, err
	}
	f, err := os.OpenFile(dst+".partial", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return exportedPart{}, err
	}
	h := exportpkg.NewHasher()
	counter := &lineCounter{}
	w := io.Writer(io.MultiWriter(f, h))
	if lines {
		w = io.MultiWriter(f, h, counter)
	}
	_, err = io.Copy(w, res.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst + ".partial")
		return exportedPart{}, err
	}
	if err := os.Rename(dst+".partial", dst); err != nil {
		return exportedPart{}, err
	}
	return exportedPart{Checksum: h.Sum(), Size: h.Size(), Documents: counter.n}, nil
}

type lineCounter struct{ n int64 }

func (l *lineCounter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			l.n++
		}
	}
	return len(p), nil
}

// fileIDsOf lists the ids of the files the documents of a part hold in the given file fields.
func fileIDsOf(path string, fields []string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	var ids []string
	add := func(v any) {
		if m, ok := v.(map[string]any); ok {
			if id, _ := m["id"].(string); id != "" && exportpkg.SafePart(id) && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	err = exportpkg.ReadDocuments(f, func(_ int, doc map[string]any) error {
		for _, field := range fields {
			switch v := doc[field].(type) {
			case []any:
				for _, e := range v {
					add(e)
				}
			default:
				add(v)
			}
		}
		return nil
	})
	return ids, err
}

// archiveDir writes dir as a gzip-compressed tar archive, readable only by its owner.
func archiveDir(dir, dst string) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	base := filepath.Base(dir)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		st, err := d.Info()
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: base + "/" + filepath.ToSlash(rel), Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
	for _, closer := range []io.Closer{tw, gz, f} {
		if cerr := closer.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

// openTarget is the server the command talks to: a running backd, or with --direct this
// process over MongoDB. The returned direct is nil for a running backd.
func openTarget(c *kli.CommandContext) (*target, func(), *direct, error) {
	isDirect, err := usesDirect(c)
	if err != nil {
		return nil, nil, nil, err
	}
	if !isDirect {
		t, err := targetOf(c, false)
		return t, func() {}, nil, err
	}
	d, err := openDirect(c)
	if err != nil {
		return nil, nil, nil, err
	}
	return d.t, d.close, d, nil
}
