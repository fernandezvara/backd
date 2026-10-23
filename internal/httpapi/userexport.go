package httpapi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/exportreadme"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// exportScanPage is how many documents one read of a user's export takes.
const exportScanPage = 200

// accountPartsDir is where the system data of a user's export lives in the package.
const accountPartsDir = "_account"

// packageObjects is what a user's export needs of the realm's file storage: reading the files
// their documents hold.
type packageObjects interface {
	Get(ctx context.Context, key, rng string) (io.ReadCloser, storage.ObjectInfo, error)
}

// personalExportParams is one personal data export to build.
type personalExportParams struct {
	Realm  string
	Svc    *auth.Users
	UserID string
	// Operator is who asked for it, for the manifest: "user:<id>" or the administrator.
	Operator string
	// Retention is how long the downloadable copy is kept, for the README; 0 for an export
	// that is handed to an administrator instead.
	Retention time.Duration
	// Objects reads the files; nil when the realm has no storage.
	Objects packageObjects
	// Version and Fingerprint are backd's version and the realm's configuration fingerprint.
	Version, Fingerprint string
	// Progress hears each step as it starts, for the job that runs the export.
	Progress func(step string)
}

// personalExportReport is what an export took.
type personalExportReport struct {
	Collections []exportpkg.Collection
	Account     map[string]int
	Files       int64
	Exclusions  []exportpkg.Exclusion
	Truncated   []string
	// Bytes is the size of the archive written.
	Bytes int64
}

// countingWriter counts what passes to the writer it wraps.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// buildPersonalExport writes a user's personal data as a zip archive to dst, in the layout of an
// export package (design/compliance.md §4.2): <database>/<collection>.jsonl for the documents the
// erasure model finds for them, <database>/_files/<id> for their files, _account/<part>.jsonl for
// the system data, manifest.json, and a README.html in their language.
//
// The scope is the user's own data, not what the rules let them read: read and reveal rules don't
// remove documents or fields, and encrypted fields come out opened. Nothing is held in memory but
// a page of documents and the ids already taken.
func (d *documents) buildPersonalExport(ctx context.Context, p personalExportParams, dst io.Writer) (*personalExportReport, error) {
	svc := p.Svc
	rl := d.reg.Realms[p.Realm]
	if rl == nil {
		return nil, errors.New("the realm no longer exists")
	}
	user, err := svc.Store.UserByID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	step := p.Progress
	if step == nil {
		step = func(string) {}
	}
	now := d.now().UTC()
	out := &countingWriter{w: dst}
	zw := zip.NewWriter(out)
	report := &personalExportReport{Account: map[string]int{}}
	checksums := map[string]string{}

	// part writes one file of the archive and records its checksum.
	part := func(name string, method uint16, fill func(w io.Writer) error) error {
		hdr := &zip.FileHeader{Name: name, Method: method, Modified: now}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		h := exportpkg.NewHasher()
		if err := fill(io.MultiWriter(w, h)); err != nil {
			return err
		}
		checksums[name] = h.Sum()
		return nil
	}

	scope := rl.PersonalScope()
	for _, e := range scope {
		if e.Export == registry.ExportExclude {
			report.Exclusions = append(report.Exclusions, exportpkg.Exclusion{What: e.Database + "/" + e.Collection, Reason: e.Reason})
			continue
		}
		if len(e.Fields) > 0 {
			report.Exclusions = append(report.Exclusions, exportpkg.Exclusion{
				What: e.Database + "/" + e.Collection + " (only the fields " + strings.Join(e.Fields, ", ") + ")", Reason: e.Reason,
			})
		}
	}

	files := map[string]exportFileRef{} // by part path, in the order found
	var fileOrder []string
	for _, e := range scope {
		if e.Export == registry.ExportExclude {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c := rl.Databases[e.Database].Collections[e.Collection]
		repo := d.store.Repository(c)
		ex, ok := repo.(storage.Exporter)
		if !ok {
			return nil, fmt.Errorf("the storage of %s.%s can't export", e.Database, e.Collection)
		}
		step("documents " + e.Database + "/" + e.Collection)
		var count int64
		name := exportpkg.CollectionPart(e.Database, e.Collection)
		seen := map[string]bool{}
		lp := &lazyPart{name: name, zw: zw, now: now}
		take := func(doc storage.Document) error {
			id, _ := doc["id"].(string)
			if id == "" || seen[id] {
				return nil
			}
			seen[id] = true
			for _, f := range e.Files {
				for _, ref := range fileRefsOf(doc[f]) {
					path := exportpkg.FilePart(e.Database, ref)
					if _, dup := files[path]; !dup {
						files[path] = exportFileRef{collection: c, id: ref}
						fileOrder = append(fileOrder, path)
					}
				}
			}
			line, err := exportpkg.EncodeDocument(shapeExported(doc, e.Fields))
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := lp.write(append(line, '\n')); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			count++
			return nil
		}
		// What the user owns, then what names them in a field the erasure model pulls them from.
		if err := scanPages(ctx, func(after string) ([]storage.Document, error) {
			return ex.ScanOwnedAfter(ctx, user.ID, after, exportScanPage)
		}, take); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if c.Erasure != nil {
			for _, field := range slices.Sorted(maps.Keys(c.Erasure.Pull)) {
				value := user.ID
				if c.Erasure.Pull[field] == registry.MatchEmail {
					value = user.Email
				}
				if err := scanPages(ctx, func(after string) ([]storage.Document, error) {
					return ex.ScanReferencingAfter(ctx, field, value, after, exportScanPage)
				}, take); err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
			}
		}
		if count == 0 {
			continue
		}
		checksums[name] = lp.sum()
		report.Collections = append(report.Collections, exportpkg.Collection{Name: e.Collection, Documents: count, Part: name})
	}

	// The files the documents hold. A file missing from storage is told in the exclusions; a
	// storage that can't be read ends the export (it is tried again).
	for _, path := range fileOrder {
		ref := files[path]
		if p.Objects == nil {
			report.Exclusions = append(report.Exclusions, exportpkg.Exclusion{What: path, Reason: "the realm's file storage is not available"})
			continue
		}
		step("file " + path)
		body, _, err := p.Objects.Get(ctx, d.objectKey(ref.collection, ref.id), "")
		if errors.Is(err, storage.ErrObjectNotFound) {
			report.Exclusions = append(report.Exclusions, exportpkg.Exclusion{What: path, Reason: "the file is no longer in the storage"})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("file %s: %w", path, err)
		}
		err = part(path, zip.Store, func(w io.Writer) error {
			_, err := io.Copy(w, body)
			return err
		})
		body.Close()
		if err != nil {
			return nil, fmt.Errorf("file %s: %w", path, err)
		}
		report.Files++
	}

	// The system data.
	step("account")
	accountParts, err := svc.AccountExport(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	var authParts []exportpkg.Collection
	for _, ap := range accountParts {
		name := accountPartsDir + "/" + ap.Name + ".jsonl"
		if err := part(name, zip.Deflate, func(w io.Writer) error {
			enc := json.NewEncoder(w)
			for _, row := range ap.Rows {
				if err := enc.Encode(row); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		report.Account[ap.Name] = len(ap.Rows)
		authParts = append(authParts, exportpkg.Collection{Name: ap.Name, Documents: int64(len(ap.Rows)), Part: name})
		if ap.Truncated {
			report.Truncated = append(report.Truncated, ap.Name)
		}
	}

	// README, then the manifest (which lists its checksums).
	step("summary")
	readme, err := d.renderExportReadme(rl, user, p, report, now)
	if err != nil {
		return nil, err
	}
	if err := part(exportreadme.ReadmeName, zip.Deflate, func(w io.Writer) error {
		_, err := io.WriteString(w, readme)
		return err
	}); err != nil {
		return nil, err
	}
	manifest := exportpkg.Manifest{
		Format: exportpkg.FormatVersion, BackdVersion: p.Version, Realm: p.Realm, CreatedAt: now,
		Operator: p.Operator, Fingerprint: p.Fingerprint, Subject: "user:" + user.ID,
		Flags:      exportpkg.Flags{Partial: true, Personal: true},
		Databases:  manifestDatabases(report.Collections),
		Encrypted:  []exportpkg.EncryptedField{},
		Checksums:  checksums,
		Files:      report.Files,
		Auth:       authParts,
		Exclusions: report.Exclusions,
	}
	raw, err := manifest.Marshal()
	if err != nil {
		return nil, err
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: exportpkg.ManifestName, Method: zip.Deflate, Modified: now})
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	report.Bytes = out.n
	return report, nil
}

// exportFileRef is a file a document holds, to be copied into the archive.
type exportFileRef struct {
	collection *registry.Collection
	id         string
}

// lazyPart is a file of the archive that is only created when its first line arrives, so a
// collection with nothing of the user's leaves no empty part behind.
type lazyPart struct {
	name string
	zw   *zip.Writer
	now  time.Time
	w    io.Writer
	h    *exportpkg.Hasher
}

func (l *lazyPart) write(line []byte) error {
	if l.w == nil {
		w, err := l.zw.CreateHeader(&zip.FileHeader{Name: l.name, Method: zip.Deflate, Modified: l.now})
		if err != nil {
			return err
		}
		l.h = exportpkg.NewHasher()
		l.w = io.MultiWriter(w, l.h)
	}
	_, err := l.w.Write(line)
	return err
}

func (l *lazyPart) sum() string { return l.h.Sum() }

// scanPages reads pages with fetch (given the last id read) until one is short, giving each
// document to take.
func scanPages(ctx context.Context, fetch func(after string) ([]storage.Document, error), take func(storage.Document) error) error {
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := fetch(after)
		if err != nil {
			return err
		}
		for _, doc := range page {
			if err := take(doc); err != nil {
				return err
			}
		}
		if len(page) < exportScanPage {
			return nil
		}
		after, _ = page[len(page)-1]["id"].(string)
	}
}

// shapeExported keeps what personal_data.fields lists of a document, plus its id and _meta.
func shapeExported(doc storage.Document, fields []string) map[string]any {
	if len(fields) == 0 {
		return doc
	}
	out := exportpkg.Select(doc, fields)
	if meta, ok := doc["_meta"]; ok {
		out["_meta"] = meta
	}
	return out
}

// fileRefsOf reads the ids of the files a file field holds: one file, or a list of them.
func fileRefsOf(v any) []string {
	one := func(x any) string {
		m, _ := x.(map[string]any)
		id, _ := m["id"].(string)
		return id
	}
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if id := one(x); id != "" {
				out = append(out, id)
			}
		}
	case []map[string]any:
		for _, x := range t {
			if id := one(x); id != "" {
				out = append(out, id)
			}
		}
	default:
		if id := one(v); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// manifestDatabases groups the exported collections by database.
func manifestDatabases(cols []exportpkg.Collection) []exportpkg.Database {
	byDB := map[string][]exportpkg.Collection{}
	for _, c := range cols {
		db, _, _ := strings.Cut(c.Part, "/")
		byDB[db] = append(byDB[db], c)
	}
	out := make([]exportpkg.Database, 0, len(byDB))
	for _, db := range slices.Sorted(maps.Keys(byDB)) {
		out = append(out, exportpkg.Database{Name: db, Collections: byDB[db]})
	}
	return out
}

// renderExportReadme prints the README of an export in the user's language: the realm's own
// template for it, else the built-in one, else the realm's default language, else English.
func (d *documents) renderExportReadme(rl *registry.Realm, user auth.User, p personalExportParams, rep *personalExportReport, now time.Time) (string, error) {
	def, _ := rl.Settings.Languages()
	var src string
pick:
	for _, loc := range []string{user.Locale, def, exportreadme.DefaultReadmeLocale} {
		if loc == "" {
			continue
		}
		if own, ok := rl.ExportReadme(loc); ok {
			src = own
			break pick
		}
		if built, ok := exportreadme.DefaultReadme(loc); ok {
			src = string(built)
			break pick
		}
	}
	data := exportreadme.ReadmeData{
		Realm: rl.Name, Email: user.Email, Generated: now, Days: int(p.Retention / (24 * time.Hour)),
		Count: rep.Account, Files: rep.Files, Truncated: rep.Truncated,
	}
	for _, c := range rep.Collections {
		data.Collections = append(data.Collections, exportreadme.ReadmePart{Part: c.Part, Documents: c.Documents})
	}
	for _, x := range rep.Exclusions {
		data.Exclusions = append(data.Exclusions, exportreadme.ReadmeExclusion{What: x.What, Reason: x.Reason})
	}
	return exportreadme.RenderReadme(src, data)
}
