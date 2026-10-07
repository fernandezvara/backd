package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func (f *filesFixture) setQuota(realm, user int64) {
	st := f.reg.Realms["acme"].Settings.Storage
	st.QuotaRealm, st.QuotaUser = realm, user
}

func quotaExceededCode(out map[string]any) bool {
	e, _ := out["error"].(map[string]any)
	return e["code"] == "quota_exceeded"
}

func TestUserQuotaIsEnforcedBeforeTheBytes(t *testing.T) {
	f := newFilesFixture(t)
	f.setQuota(0, 100)
	id := f.newDoc(t, "library")
	// 36 bytes fit; 36 + 36 + 36 passes 100 for this user.
	for i := 0; i < 2; i++ {
		if rec, out := f.upload(t, f.ada, "library", id, "receipts", "r.png", "image/png", pngBytes(20), nil); rec.Code != 201 {
			t.Fatalf("upload %d: %d %v", i, rec.Code, out)
		}
	}
	rec, out := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(20), nil)
	if rec.Code != http.StatusRequestEntityTooLarge || !quotaExceededCode(out) {
		t.Fatalf("over the user's quota: %d %v", rec.Code, out)
	}
	if len(f.s3.Keys()) != 2 {
		t.Errorf("the refused file was stored: %v", f.s3.Keys())
	}
	// Another user has their own.
	bobDoc := f.newDocAs(t, f.bob, "library")
	if rec, _ := f.upload(t, f.bob, "library", bobDoc, "receipts", "r.png", "image/png", pngBytes(20), nil); rec.Code != 201 {
		t.Errorf("another user: %d", rec.Code)
	}
	// Removing a file gives the room back.
	_, doc := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	first := doc["receipts"].([]any)[0].(map[string]any)["id"].(string)
	if rec, _ := f.doRaw(t, "DELETE", "/v1/acme/app/library/"+id+"/_files/receipts/"+first, nil, map[string]string{"Authorization": "Bearer " + f.ada}); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec, _ := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(20), nil); rec.Code != 201 {
		t.Errorf("after removing one: %d", rec.Code)
	}
}

func TestReplacingCountsOnlyTheDifference(t *testing.T) {
	f := newFilesFixture(t)
	f.setQuota(0, 100)
	id := f.newDoc(t, "library")
	if rec, _ := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(64), nil); rec.Code != 201 { // 80 bytes
		t.Fatal(rec.Code)
	}
	// 90 would not fit beside the 80, but it replaces them.
	if rec, out := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(74), nil); rec.Code != 201 {
		t.Errorf("a replace that fits once the old file is gone: %d %v", rec.Code, out)
	}
	if rec, out := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(90), nil); rec.Code != http.StatusRequestEntityTooLarge || !quotaExceededCode(out) {
		t.Errorf("a replace that doesn't: %d %v", rec.Code, out)
	}
}

func TestRealmQuotaCountsEveryone(t *testing.T) {
	f := newFilesFixture(t)
	f.setQuota(100, 0)
	a, b := f.newDoc(t, "library"), f.newDocAs(t, f.bob, "library")
	if rec, _ := f.upload(t, f.ada, "library", a, "avatar", "a.png", "image/png", pngBytes(44), nil); rec.Code != 201 { // 60
		t.Fatal(rec.Code)
	}
	if rec, out := f.upload(t, f.bob, "library", b, "avatar", "b.png", "image/png", pngBytes(44), nil); rec.Code != http.StatusRequestEntityTooLarge || !quotaExceededCode(out) {
		t.Errorf("past the realm's: %d %v", rec.Code, out)
	}
	// A key owns nothing, and the realm's quota still applies.
	if rec, out := f.upload(t, f.key, "library", a, "receipts", "k.png", "image/png", pngBytes(44), nil); rec.Code != http.StatusRequestEntityTooLarge || !quotaExceededCode(out) {
		t.Errorf("a key past the realm's: %d %v", rec.Code, out)
	}
	if rec, _ := f.upload(t, f.key, "library", a, "receipts", "k.png", "image/png", pngBytes(10), nil); rec.Code != 201 {
		t.Errorf("a key within it: %d", rec.Code)
	}
}

// A body of unknown length is stopped where the quota ends, not after it was stored.
func TestAnUnknownLengthIsStoppedAtTheQuota(t *testing.T) {
	f := newFilesFixture(t)
	f.setQuota(0, 100)
	id := f.newDoc(t, "library")
	req := httptest.NewRequest("POST", "/v1/acme/app/library/"+id+"/_files/receipts", bytes.NewReader(pngBytes(300)))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+f.ada)
	req.Header.Set("Content-Type", "image/png")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(rec.Body.Bytes(), []byte("quota_exceeded")) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestQuotaOnDirectAndPendingUploads(t *testing.T) {
	f := newFilesFixture(t)
	f.setQuota(0, 100)
	// Pending (proxy): over.
	rec, out := f.doRaw(t, "POST", "/v1/acme/app/forms/_files/scan/uploads", pngBytes(200), map[string]string{"Content-Type": "image/png", "Authorization": "Bearer " + f.ada})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("pending: %d %v", rec.Code, out)
	}
	// Direct to a document, and pending direct: the declared size.
	big := declare("c.png", pngBytes(90), "image/png") // 106 bytes
	id := f.videoDoc(t)
	rec, out = f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/clips/uploads", big)
	if rec.Code != http.StatusRequestEntityTooLarge || !quotaExceededCode(out) {
		t.Errorf("direct: %d %v", rec.Code, out)
	}
	rec, out = f.startOn(t, f.bob, "/v1/acme/app/videos/_files/clip/uploads", big)
	if rec.Code != http.StatusRequestEntityTooLarge || !quotaExceededCode(out) {
		t.Errorf("pending direct: %d %v", rec.Code, out)
	}
	// Room that was there when the upload was made and isn't when it is attached.
	f.setQuota(0, 0)
	pid, tok := f.pending(t, f.ada, "forms", "scan", pngBytes(60)) // 76 bytes
	f.setQuota(0, 50)
	if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(pid, tok)+`}`); code != http.StatusRequestEntityTooLarge || !quotaExceededCode(out) {
		t.Errorf("attaching: %d %v", code, out)
	}
	f.setQuota(0, 150)
	if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(pid, tok)+`}`); code != 201 {
		t.Errorf("attaching with room: %d %v", code, out)
	}
}

// The status shows the limits beside the totals.
func TestStorageStatusShowsTheQuota(t *testing.T) {
	f := newFilesFixture(t)
	f.setQuota(1000, 200)
	_, out := f.doRaw(t, "GET", "/v1/acme/_admin/storage", nil, map[string]string{"Authorization": "Bearer " + f.key})
	q, _ := out["quota"].(map[string]any)
	if q["realm"] != float64(1000) || q["user"] != float64(200) {
		t.Errorf("quota: %v", out["quota"])
	}
	f.setQuota(0, 0)
	_, out = f.doRaw(t, "GET", "/v1/acme/_admin/storage", nil, map[string]string{"Authorization": "Bearer " + f.key})
	if q, _ := out["quota"].(map[string]any); q["realm"] != nil || q["user"] != nil {
		t.Errorf("no quota: %v", out["quota"])
	}
}
