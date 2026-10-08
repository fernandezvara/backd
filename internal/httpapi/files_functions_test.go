package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// Functions reach files through the internal listener with a callback token: the same
// routes as HTTP, with the caller's identity (ctx.db acts as the user, ctx.admin.db skips
// the rules), and with downloads streamed by backd: a function can't reach the bucket.
func TestFunctionsUseTheFileRoutes(t *testing.T) {
	f := newFilesFixture(t)
	exp := f.clock.Add(time.Minute)
	user := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", UserID: f.adaID, Expires: exp})
	admin := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", Admin: true, Expires: exp})
	do := func(token, method, path string, body []byte, ct string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.internal.ServeHTTP(rec, req)
		return rec
	}
	id := f.newDoc(t, "library") // ada's document
	base := "/v1/acme/app/library/" + id + "/_files/"

	// As the user: the update rule applies, so the avatar is hers to set and the thumbnail
	// (which the rules reserve) is not.
	rec := do(user, "POST", base+"avatar?name=a.png", pngBytes(20), "image/png")
	if rec.Code != 201 {
		t.Fatalf("ctx.db put: %d %s", rec.Code, rec.Body)
	}
	if rec := do(user, "POST", base+"thumbnail", pngBytes(5), "image/png"); rec.Code != http.StatusForbidden {
		t.Errorf("ctx.db put to a reserved field: %d", rec.Code)
	}
	// As a function with admin access: no rule, the reserved field is written.
	rec = do(admin, "POST", base+"thumbnail?name=t.png", pngBytes(5), "image/png")
	if rec.Code != 201 {
		t.Fatalf("ctx.admin.db put: %d %s", rec.Code, rec.Body)
	}
	_, doc := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	avatarID := doc["avatar"].(map[string]any)["id"].(string)
	if doc["thumbnail"] == nil {
		t.Fatal("the thumbnail isn't there")
	}
	// Reading: the bytes come through backd even though the field's downloads are
	// redirects for everyone else.
	rec = do(user, "GET", base+"avatar/"+avatarID, nil, "")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes(20)) || rec.Header().Get("Location") != "" {
		t.Errorf("ctx.db get: %d, %d bytes, location %q", rec.Code, rec.Body.Len(), rec.Header().Get("Location"))
	}
	// A user who can't read the document gets nothing.
	other := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", UserID: f.bobID, Expires: exp})
	if rec := do(other, "GET", base+"avatar/"+avatarID, nil, ""); rec.Code != 404 {
		t.Errorf("another user's get: %d", rec.Code)
	}
	// Links: a presigned one is the bucket's; a backd one needs the public address.
	rec = do(user, "GET", base+"avatar/"+avatarID+"?link=json", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), f.s3.URL) {
		t.Errorf("link: %d %s", rec.Code, rec.Body)
	}
	_, doc = f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	f.upload(t, f.ada, "library", id, "manual", "m.txt", "text/plain", []byte("manual"), nil)
	_, doc = f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	manualID := doc["manual"].(map[string]any)["id"].(string)
	rec = do(user, "GET", base+"manual/"+manualID+"?link=json", nil, "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "BACKD_URL") {
		t.Errorf("a backd link without BACKD_URL: %d %s", rec.Code, rec.Body)
	}
	// Removing: under the rules for the user, free for the function.
	if rec := do(user, "DELETE", base+"thumbnail", nil, ""); rec.Code != http.StatusForbidden {
		t.Errorf("ctx.db clear of a reserved field: %d", rec.Code)
	}
	if rec := do(admin, "DELETE", base+"thumbnail", nil, ""); rec.Code != 200 {
		t.Errorf("ctx.admin.db clear: %d", rec.Code)
	}
	// The identity is recorded like any upload's.
	e := f.journal(avatarID)
	if e.Caller != "user:"+f.adaID || e.Status != auth.JournalAttached {
		t.Errorf("journal: %+v", e)
	}
}

// A function asks for a version over the internal listener: as the user the update rule
// applies, as a function with admin access it doesn't, and either way a worker makes it.
func TestFunctionsRemakeVersionsThroughTheInternalListener(t *testing.T) {
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	docID, fileID := f.madePicture(t, w, "picture")
	runImageJobs(t, w)
	runWorker(t, w)
	exp := f.clock.Add(time.Minute)
	user := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", UserID: f.adaID, Expires: exp})
	admin := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", Admin: true, Expires: exp})
	other := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", UserID: f.bobID, Expires: exp})
	do := func(token, method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/acme/app/library/"+docID+"/_files/picture/"+fileID+"/versions/thumb", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.internal.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(user, "POST", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ready"`) {
		t.Errorf("ctx.db: %d %s", rec.Code, rec.Body)
	}
	if rec := do(admin, "POST", ""); rec.Code != 200 {
		t.Errorf("ctx.admin.db: %d %s", rec.Code, rec.Body)
	}
	if rec := do(other, "POST", ""); rec.Code != 404 {
		t.Errorf("a user who can't read the document: %d", rec.Code)
	}
	if rec := do(admin, "DELETE", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"pending"`) {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}
}
