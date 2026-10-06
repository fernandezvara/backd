package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// pending uploads a file for a collection's field and returns the upload's id and token.
func (f *filesFixture) pending(t *testing.T, cred, collection, field string, data []byte) (id, token string) {
	t.Helper()
	hdr := map[string]string{"Content-Type": "image/png"}
	if cred != "" {
		hdr["Authorization"] = "Bearer " + cred
	}
	rec, out := f.doRaw(t, "POST", "/v1/acme/app/"+collection+"/_files/"+field+"/uploads?name=scan.png", data, hdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("pending upload: %d %s", rec.Code, rec2s(rec.Body.String()))
	}
	id, _ = out["upload_id"].(string)
	token, _ = out["upload_token"].(string)
	return id, token
}

func rec2s(s string) string { return s }

func ref(id, token string) string {
	b, _ := json.Marshal(map[string]string{"upload": id, "token": token})
	return string(b)
}

func (f *filesFixture) journal(id string) auth.FileJournalEntry {
	e, _ := f.svc.Store.(interface {
		JournalEntry(string) (auth.FileJournalEntry, bool)
	}).JournalEntry(id)
	return e
}

func TestCreateWithAPendingUpload(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	data := pngBytes(30)

	rec, out := f.doRaw(t, "POST", "/v1/acme/app/forms/_files/scan/uploads?name=../scan.png", data, map[string]string{"Content-Type": "image/png", "Authorization": "Bearer " + f.ada})
	id, token := out["upload_id"].(string), out["upload_token"].(string)
	file, _ := out["file"].(map[string]any)
	if rec.Code != 201 || !strings.HasPrefix(id, "fl_") || !strings.HasPrefix(token, "fut_") || out["expires_at"] == nil || rec.Header().Get("Cache-Control") != "no-store" ||
		file["name"] != "scan.png" || file["type"] != "image/png" || file["size"] != float64(len(data)) || file["sha256"] != sha(data) {
		t.Fatalf("pending upload: %d %v", rec.Code, out)
	}
	// The object is stored under the file's key, the journal holds only the token's hash,
	// and no document exists yet.
	if keys := f.s3.Keys(); len(keys) != 1 || keys[0] != "t/acme/app/forms/"+id {
		t.Fatalf("stored: %v", keys)
	}
	e := f.journal(id)
	if !e.Pending || e.Status != auth.JournalStored || e.TokenHash == "" || e.TokenHash == token || strings.Contains(e.TokenHash, "fut_") || e.Owner == "" {
		t.Errorf("journal: %+v", e)
	}
	_, list := f.as(t, f.ada, "GET", "/v1/acme/app/forms", "")
	if len(list["items"].([]any)) != 0 {
		t.Errorf("a document exists before the create: %v", list)
	}

	// A write names it: the file's details are in the created document, which is valid
	// although the field is required.
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "claim", "scan": `+ref(id, token)+`}`)
	scan, _ := doc["scan"].(map[string]any)
	if code != 201 || scan["id"] != id || scan["name"] != "scan.png" || scan["sha256"] != sha(data) || scan["size"] != float64(len(data)) || scan["upload"] != nil || scan["token"] != nil {
		t.Fatalf("create: %d %v", code, doc)
	}
	if e := f.journal(id); e.Status != auth.JournalAttached || e.DocumentID != doc["id"] {
		t.Errorf("journal after: %+v", e)
	}
	// And the file is the document's like any other.
	rec, _ = f.doRaw(t, "GET", "/v1/acme/app/forms/"+doc["id"].(string)+"/_files/scan/"+id+"?link=json", nil, map[string]string{"Authorization": "Bearer " + f.ada})
	if rec.Code != 200 {
		t.Errorf("download: %d", rec.Code)
	}

	// Without a file the required field is missing, as in any create.
	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "no file"}`); code != 400 {
		t.Errorf("required file field missing: %d", code)
	}
}

func TestPendingUploadReferencesAreRefused(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	post := func(who, body string) (int, map[string]any) {
		t.Helper()
		return f.as(t, who, "POST", "/v1/acme/app/forms", body)
	}
	reasons := map[string]string{}
	refuse := func(name, who, body string) {
		t.Helper()
		code, out := post(who, body)
		if code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
			return
		}
		if d, _ := out["error"].(map[string]any)["details"].([]any); len(d) == 1 {
			reasons[name] = d[0].(map[string]any)["reason"].(string)
		}
	}
	id, token := f.pending(t, f.ada, "forms", "scan", pngBytes(5))

	refuse("a wrong token", f.ada, `{"title": "t", "scan": `+ref(id, "fut_wrong")+`}`)
	refuse("another user's upload", f.bob, `{"title": "t", "scan": `+ref(id, token)+`}`)
	refuse("an upload that doesn't exist", f.ada, `{"title": "t", "scan": `+ref("fl_nothing", token)+`}`)
	extraID, extraTok := f.pending(t, f.ada, "forms", "extras", pngBytes(5))
	refuse("the wrong field", f.ada, `{"title": "t", "scan": `+ref(extraID, extraTok)+`}`)
	otherID, otherTok := f.pending(t, f.ada, "library", "avatar", pngBytes(5))
	refuse("the wrong collection", f.ada, `{"title": "t", "scan": `+ref(otherID, otherTok)+`}`)
	// Not finished: journaled, never completed.
	_ = f.svc.JournalPendingUpload(context.Background(), auth.FileJournalEntry{ID: "fl_unfinished", Database: "app", Collection: "forms", Field: "scan", Key: "k", TokenHash: auth.HashUploadToken("fut_x"), CallerKey: "user:x"}, time.Hour)
	refuse("an unfinished upload", f.ada, `{"title": "t", "scan": `+ref("fl_unfinished", "fut_x")+`}`)
	// Malformed references.
	refuse("no token", f.ada, `{"title": "t", "scan": {"upload": "`+id+`"}}`)
	refuse("no id", f.ada, `{"title": "t", "scan": {"upload": "", "token": "x"}}`)
	refuse("two files in a single field", f.ada, `{"title": "t", "scan": [`+ref(id, token)+`, `+ref(extraID, extraTok)+`]}`)
	refuse("the same upload twice", f.ada, `{"title": "t", "extras": [`+ref(extraID, extraTok)+`, `+ref(extraID, extraTok)+`]}`)
	// None of those used the upload.
	if e := f.journal(id); e.Status != auth.JournalStored {
		t.Fatalf("a refused write consumed the upload: %+v", e)
	}
	// The reasons that say nothing about which part was wrong are the same.
	for _, name := range []string{"a wrong token", "another user's upload", "an upload that doesn't exist", "the wrong field", "the wrong collection", "an unfinished upload"} {
		if reasons[name] != uploadInvalid {
			t.Errorf("%s: %q", name, reasons[name])
		}
	}

	// It is used once.
	if code, out := post(f.ada, `{"title": "ok", "scan": `+ref(id, token)+`}`); code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	refuse("a second use", f.ada, `{"title": "again", "scan": `+ref(id, token)+`}`)

	// It expires: after pending_ttl nothing attaches it, and a worker deletes the object.
	id2, token2 := f.pending(t, f.ada, "forms", "scan", pngBytes(6))
	*f.clock = f.clock.Add(61 * time.Minute)
	refuse("an expired upload", f.ada, `{"title": "late", "scan": `+ref(id2, token2)+`}`)
	if reasons["an expired upload"] != uploadInvalid {
		t.Errorf("expired: %q", reasons["an expired upload"])
	}
}

func TestExpiredPendingUploadsAreCleanedUp(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	usedID, usedTok := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	idleID, _ := f.pending(t, f.ada, "forms", "scan", pngBytes(6))
	if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(usedID, usedTok)+`}`); code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	w.FilesDue(context.Background())
	if len(f.s3.Keys()) != 2 {
		t.Fatalf("a live upload was deleted: %v", f.s3.Keys())
	}
	*f.clock = f.clock.Add(2 * time.Hour)
	w.FilesDue(context.Background())
	keys := f.s3.Keys()
	if len(keys) != 1 || !strings.HasSuffix(keys[0], usedID) {
		t.Errorf("after expiry: %v (the unused %s should be gone, the used one kept)", keys, idleID)
	}
	if e := f.journal(idleID); e.Status != auth.JournalFailed {
		t.Errorf("journal of the unused: %+v", e)
	}
	if e := f.journal(usedID); e.Status != auth.JournalAttached {
		t.Errorf("journal of the used: %+v", e)
	}
}

func TestAnAttachInterruptedIsRecovered(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	ctx := context.Background()
	id, token := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(id, token)+`}`)
	if code != 201 {
		t.Fatal(code)
	}
	// The process died after the document was written and before the journal said so.
	_ = f.svc.Store.SetFileJournalStatus(ctx, id, auth.JournalAttaching, doc["id"].(string), *f.clock, f.clock.Add(time.Hour))
	// And another died after claiming, before the document was written: nothing references it.
	lostID, lostTok := f.pending(t, f.ada, "forms", "scan", pngBytes(9))
	if _, err := f.svc.ClaimPendingUpload(ctx, lostID, lostTok, "doc_never_written"); err != nil {
		t.Fatal(err)
	}
	*f.clock = f.clock.Add(2 * time.Hour)
	w.FilesDue(ctx)
	if e := f.journal(id); e.Status != auth.JournalAttached {
		t.Errorf("a file the document holds: %+v", e)
	}
	keys := f.s3.Keys()
	if len(keys) != 1 || !strings.HasSuffix(keys[0], id) {
		t.Errorf("objects: %v: the referenced one stays, the lost one goes", keys)
	}
	if e := f.journal(lostID); e.Status != auth.JournalFailed {
		t.Errorf("the lost claim: %+v", e)
	}
}

func TestAnonymousCallersAndTheCreateRule(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	// forms admits anonymous creates: an anonymous caller uploads and attaches.
	id, token := f.pending(t, "", "forms", "scan", pngBytes(5))
	code, doc := f.doRaw(t, "POST", "/v1/acme/app/forms", []byte(`{"title": "anon", "scan": `+ref(id, token)+`}`), map[string]string{"Content-Type": "application/json"})
	if code.Code != 201 || doc["scan"].(map[string]any)["id"] != id {
		t.Fatalf("anonymous create: %d %v", code.Code, doc)
	}
	// An anonymous upload can be attached by whoever holds the token, signed in or not.
	id, token = f.pending(t, "", "forms", "scan", pngBytes(6))
	if c, out := f.as(t, f.bob, "POST", "/v1/acme/app/forms", `{"title": "bob", "scan": `+ref(id, token)+`}`); c != 201 {
		t.Errorf("a signed-in user with an anonymous upload: %d %v", c, out)
	}
	// library's create rule needs a user: anonymous callers can't start an upload there.
	rec, _ := f.doRaw(t, "POST", "/v1/acme/app/library/_files/avatar/uploads", pngBytes(5), map[string]string{"Content-Type": "image/png"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous upload where the create rule refuses anonymous: %d", rec.Code)
	}
	// A collection with no create rule at all admits nobody.
	f.reg.Realms["acme"].Databases["app"].Collections["gallery"].Rules = nil
	rec, _ = f.doRaw(t, "POST", "/v1/acme/app/gallery/_files/avatar/uploads", pngBytes(5), map[string]string{"Content-Type": "image/png", "Authorization": "Bearer " + f.ada})
	if rec.Code != http.StatusForbidden {
		t.Errorf("no create rule: %d", rec.Code)
	}
}

func TestPendingUploadLimits(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	var ids, tokens []string
	for i := 0; i < auth.MaxOpenPendingUploads; i++ {
		id, tok := f.pending(t, f.ada, "forms", "scan", pngBytes(i))
		ids, tokens = append(ids, id), append(tokens, tok)
	}
	hdr := map[string]string{"Content-Type": "image/png", "Authorization": "Bearer " + f.ada}
	rec, out := f.doRaw(t, "POST", "/v1/acme/app/forms/_files/scan/uploads", pngBytes(1), hdr)
	if rec.Code != 429 || out["error"].(map[string]any)["code"] != "too_many_requests" {
		t.Fatalf("the 21st open upload: %d %v", rec.Code, out)
	}
	if len(f.s3.Keys()) != auth.MaxOpenPendingUploads {
		t.Errorf("a refused upload stored bytes: %d objects", len(f.s3.Keys()))
	}
	// Another caller has their own count.
	f.pending(t, f.bob, "forms", "scan", pngBytes(2))
	// Using one frees a place.
	if c, out := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(ids[0], tokens[0])+`}`); c != 201 {
		t.Fatalf("create: %d %v", c, out)
	}
	if rec, _ := f.doRaw(t, "POST", "/v1/acme/app/forms/_files/scan/uploads", pngBytes(3), hdr); rec.Code != 201 {
		t.Errorf("after one was used: %d", rec.Code)
	}
	// So does expiry.
	*f.clock = f.clock.Add(2 * time.Hour)
	if rec, _ := f.doRaw(t, "POST", "/v1/acme/app/forms/_files/scan/uploads", pngBytes(4), hdr); rec.Code != 201 {
		t.Errorf("after they expired: %d", rec.Code)
	}
	// The same refusals as any upload apply before and while storing.
	for name, c := range map[string]struct {
		field, ct string
		body      []byte
		want      int
	}{
		"a type the field doesn't take": {"scan", "text/plain", []byte("plain text"), 415},
		"a file over max_size":          {"scan", "image/png", append(pngBytes(0), make([]byte, 5000)...), 413},
		"an unknown field":              {"nope", "image/png", pngBytes(1), 404},
	} {
		h := map[string]string{"Content-Type": c.ct, "Authorization": "Bearer " + f.ada}
		if rec, _ := f.doRaw(t, "POST", "/v1/acme/app/forms/_files/"+c.field+"/uploads", c.body, h); rec.Code != c.want {
			t.Errorf("%s: %d, want %d", name, rec.Code, c.want)
		}
	}
}

func TestPendingUploadsOnUpdates(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	ada := map[string]string{"Authorization": "Bearer " + f.ada}
	id, tok := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	_, doc := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(id, tok)+`}`)
	docID := doc["id"].(string)
	path := "/v1/acme/app/forms/" + docID

	// PATCH with a reference replaces a single field's file, whose object is then deleted.
	id2, tok2 := f.pending(t, f.ada, "forms", "scan", pngBytes(7))
	code, out := f.as(t, f.ada, "PATCH", path, `{"scan": `+ref(id2, tok2)+`}`)
	if code != 200 || out["scan"].(map[string]any)["id"] != id2 || out["_meta"].(map[string]any)["version"] != float64(2) {
		t.Fatalf("patch: %d %v", code, out)
	}
	dels := f.svc.Store.(interface{ Deletions() []auth.FileDeletion }).Deletions()
	if len(dels) != 1 || !strings.HasSuffix(dels[0].Key, id) || dels[0].Reason != "replaced" {
		t.Errorf("deletions: %+v", dels)
	}
	// Details sent back by a client are ignored, not mistaken for a reference.
	code, out = f.as(t, f.ada, "PUT", path, `{"title": "put", "scan": {"id": "fl_forged", "name": "x", "size": 1, "type": "image/png", "sha256": "`+strings.Repeat("a", 64)+`", "uploaded_at": "2026-01-01T00:00:00Z"}}`)
	if code != 200 || out["scan"].(map[string]any)["id"] != id2 {
		t.Errorf("put kept the files: %d %v", code, out)
	}
	// A multiple field gets the new files after what it holds, up to max_files.
	a, at := f.pending(t, f.ada, "forms", "extras", pngBytes(1))
	b, bt := f.pending(t, f.ada, "forms", "extras", pngBytes(2))
	c3, ct := f.pending(t, f.ada, "forms", "extras", pngBytes(3))
	if code, out = f.as(t, f.ada, "PATCH", path, `{"extras": `+ref(a, at)+`}`); code != 200 || len(out["extras"].([]any)) != 1 {
		t.Fatalf("one into extras: %d %v", code, out)
	}
	code, out = f.as(t, f.ada, "PATCH", path, `{"extras": [`+ref(b, bt)+`, `+ref(c3, ct)+`]}`)
	if code != 409 || out["error"].(map[string]any)["code"] != "too_many_files" {
		t.Errorf("over max_files: %d %v", code, out)
	}
	if e := f.journal(b); e.Status != auth.JournalStored {
		t.Errorf("a refused write took the upload: %+v", e)
	}
	if code, out = f.as(t, f.ada, "PATCH", path, `{"extras": `+ref(b, bt)+`}`); code != 200 || len(out["extras"].([]any)) != 2 {
		t.Errorf("a second into extras: %d %v", code, out)
	}
	// If-Match applies as it always does.
	if rec, _ := f.doRaw(t, "PATCH", path, []byte(`{"extras": `+ref(c3, ct)+`}`), map[string]string{"Authorization": ada["Authorization"], "Content-Type": "application/merge-patch+json", "If-Match": `"1"`}); rec.Code != 412 {
		t.Errorf("stale If-Match: %d", rec.Code)
	}
	if e := f.journal(c3); e.Status != auth.JournalStored {
		t.Errorf("a refused write took the upload: %+v", e)
	}
	// Someone else's document: the read rule hides it, the upload stays unused.
	bobID, bobTok := f.pending(t, f.bob, "forms", "extras", pngBytes(4))
	if code, _ := f.as(t, f.bob, "PATCH", path, `{"extras": `+ref(bobID, bobTok)+`}`); code != 404 {
		t.Errorf("another user's document: %d", code)
	}
}

// Rules see the final document, with the file in it, for every caller the same way.
func TestRulesSeeThePendingFile(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	// library's create rule: nobody sets thumbnail but a function.
	id, tok := f.pending(t, f.ada, "library", "thumbnail", pngBytes(5))
	code, out := f.as(t, f.ada, "POST", "/v1/acme/app/library", `{"title": "t", "thumbnail": `+ref(id, tok)+`}`)
	if code != http.StatusForbidden {
		t.Fatalf("a reserved field on create: %d %v", code, out)
	}
	if e := f.journal(id); e.Status != auth.JournalStored {
		t.Errorf("a refused write took the upload: %+v", e)
	}
	// On update too: changed() holds the field.
	doc := f.newDoc(t, "library")
	if code, _ := f.as(t, f.ada, "PATCH", "/v1/acme/app/library/"+doc, `{"thumbnail": `+ref(id, tok)+`}`); code != http.StatusForbidden {
		t.Errorf("a reserved field on update: %d", code)
	}
	// An upload a user made is theirs alone: a key can't attach it. A key skips rules
	// for its own.
	hdr := map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/merge-patch+json"}
	if rec, _ := f.doRaw(t, "PATCH", "/v1/acme/app/library/"+doc, []byte(`{"thumbnail": `+ref(id, tok)+`}`), hdr); rec.Code != 400 {
		t.Errorf("a key attaching a user's upload: %d", rec.Code)
	}
	keyID, keyTok := f.pending(t, f.key, "library", "thumbnail", pngBytes(8))
	if rec, _ := f.doRaw(t, "PATCH", "/v1/acme/app/library/"+doc, []byte(`{"thumbnail": `+ref(keyID, keyTok)+`}`), hdr); rec.Code != 200 {
		t.Errorf("a key attaches it: %d", rec.Code)
	}
}

// A write that fails after claiming gives the upload back.
func TestAFailedWriteReleasesThePendingUpload(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id, tok := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	f.store.fail = errConflictForContract
	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(id, tok)+`}`); code != http.StatusConflict {
		t.Fatalf("a failing store: %d", code)
	}
	f.store.fail = nil
	if e := f.journal(id); e.Status != auth.JournalStored || e.DocumentID != "" {
		t.Fatalf("released: %+v", e)
	}
	if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(id, tok)+`}`); code != 201 {
		t.Errorf("after it: %d %v", code, out)
	}
}
