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

// Support produces the export of a user for a formal request: the package goes to the
// administrator, the user is told unless the administrator says why not.
func TestAdministratorExportsAUsersData(t *testing.T) {
	f := newExportFixture(t)
	ctx := context.Background()
	user := admin + "/users/" + f.adaID
	seedDoc(t, f.rulesFixture, "contacts", "k1", f.adaID, map[string]any{"title": "Ada's card", "body": "likes tea", "internal_note": "STAFF-ONLY"})

	// A realm that does not export refuses, whoever asks.
	if rec, out := f.doH(t, "POST", user+"/export", "", bearer(f.key)); rec.Code != http.StatusNotFound {
		t.Fatalf("export off: %d %v", rec.Code, out)
	}
	f.enable()

	// It needs full admin access: an administrator of users alone is refused.
	f.bobHolds(t, "support")
	if code, out := f.as(t, f.bob, "POST", user+"/export", ""); code != http.StatusForbidden || !strings.Contains(out["error"].(map[string]any)["message"].(string), "full admin access") {
		t.Errorf("support asks: %d %v", code, out)
	}
	// ...and a fresh proof, for a signed-in administrator.
	f.bobHolds(t, "staff")
	if rec, out := f.doH(t, "POST", user+"/export", "", bearer(f.bob)); rec.Code != http.StatusUnauthorized || errCode(out) != "reauth_required" {
		t.Errorf("without a proof: %d %v", rec.Code, out)
	}

	// Not telling the user needs the reason, and a reason only goes with not telling.
	if rec, out := f.doH(t, "POST", user+"/export", `{"no_notify": true}`, bearer(f.key)); rec.Code != 400 {
		t.Errorf("silent without a reason: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", user+"/export", `{"reason": "x"}`, bearer(f.key)); rec.Code != 400 {
		t.Errorf("a reason alone: %d %v", rec.Code, out)
	}

	// A silent export: the user is told nothing, and is not offered the package.
	rec, out := f.doH(t, "POST", user+"/export", `{"no_notify": true, "reason": "fraud inquiry, the user must not be warned", "ticket": "LEGAL-7"}`, bearer(f.key))
	if rec.Code != http.StatusAccepted || out["silent"] != true || out["ticket"] != "LEGAL-7" || out["notified"] != nil {
		t.Fatalf("silent export: %d %v", rec.Code, out)
	}
	silentID := out["id"].(string)
	if rec, _ := f.doH(t, "POST", user+"/export", "", bearer(f.key)); rec.Code != http.StatusConflict {
		t.Errorf("a second export while one is running: %d", rec.Code)
	}
	if !f.w.RunOnce(ctx) {
		t.Fatal("no export job")
	}
	for _, kind := range []string{"data-export-ready", "data-export-made"} {
		if jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Origin: "backd:email." + kind}); len(jobs) != 0 {
			t.Errorf("a %s email was queued for a silent export", kind)
		}
	}
	rec, got := f.doH(t, "GET", user+"/exports/"+silentID, "", bearer(f.key))
	if rec.Code != 200 || got["status"] != "ready" || got["requested_by"] == "user" || got["silent"] != true || got["reason"] == nil {
		t.Fatalf("the administrator's view: %d %v", rec.Code, got)
	}
	// It is not in the user's list, and the user cannot reach it.
	if rec, mine := f.doH(t, "GET", exportsPath+"s", "", bearer(f.ada)); rec.Code != 200 || len(mine["items"].([]any)) != 0 {
		t.Errorf("the user's list: %v", mine)
	}
	if code, _ := f.as(t, f.ada, "GET", exportsPath+"s/"+silentID+"/download", ""); code != http.StatusNotFound {
		t.Errorf("the user reaches an administrator's export: %d", code)
	}

	// Downloading needs full access too; it comes through the API as a zip.
	f.bobHolds(t, "support")
	if code, _ := f.as(t, f.bob, "GET", user+"/exports/"+silentID+"/download", ""); code != http.StatusForbidden {
		t.Errorf("support downloads: %d", code)
	}
	f.bobHolds(t, "staff")
	rec, _ = f.doH(t, "GET", user+"/exports/"+silentID+"/download", "", map[string]string{"Authorization": "Bearer " + f.bob, reauthHeader: f.freshProof(t, f.bob)})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "acme-data-export-") {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	pkg := openZip(t, rec.Body.Bytes())
	if !strings.Contains(string(pkg["app/contacts.jsonl"]), "likes tea") || strings.Contains(string(pkg["app/contacts.jsonl"]), "STAFF-ONLY") {
		t.Errorf("contacts = %s", pkg["app/contacts.jsonl"])
	}
	var m struct{ Operator string }
	if err := json.Unmarshal(pkg["manifest.json"], &m); err != nil || !strings.HasPrefix(m.Operator, "key:") {
		t.Errorf("the manifest names who made it: %q %v", m.Operator, err)
	}

	// The audit trail keeps them apart: the silent request has its reason, and the download who took it.
	silent, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditDataExportSilent})
	if len(silent) != 1 || silent[0].Details["reason"] != "fraud inquiry, the user must not be warned" || silent[0].Details["ticket"] != "LEGAL-7" || silent[0].Target != "user:"+f.adaID {
		t.Errorf("silent audit: %+v", silent)
	}
	if told, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditDataExportRequested}); len(told) != 0 {
		t.Errorf("a silent export in the list of those the user was told about: %+v", told)
	}
	dl, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditDataExportDownloaded})
	if len(dl) != 1 || !strings.HasPrefix(dl[0].Details["by"].(string), "user:") {
		t.Errorf("download audit: %+v", dl)
	}

	// An export the user is told about: the email says so, with the reference, and the package is still the administrator's.
	rec, out = f.doH(t, "POST", user+"/export", `{"ticket": "REQ-12"}`, bearer(f.key))
	if rec.Code != http.StatusAccepted || out["notified"] != true || out["silent"] != nil {
		t.Fatalf("export: %d %v", rec.Code, out)
	}
	madeID := out["id"].(string)
	var told emailInput
	for i := 0; i < 3 && told.Kind == ""; i++ {
		if !f.w.RunOnce(ctx) {
			t.Fatal("no job to run")
		}
		f.runner.mu.Lock()
		delivered := len(f.runner.reqs)
		f.runner.mu.Unlock()
		if delivered == 0 {
			continue // the export job ran: it calls no function
		}
		if in := deliveredEmail(t, f.runner.last()); in.Kind == "data-export-made" {
			told = in
		}
	}
	if told.To[0].Email != "ada@example.com" || !strings.Contains(told.Text, "REQ-12") || strings.Contains(told.Text, ".zip") {
		t.Errorf("email: %+v", told)
	}
	if code, _ := f.as(t, f.ada, "GET", exportsPath+"s/"+madeID, ""); code != http.StatusNotFound {
		t.Errorf("the user opens an export made for them: %d", code)
	}
	if code, _ := f.as(t, f.bob, "GET", user+"/exports", ""); code != 200 {
		t.Errorf("list: %d", code)
	}

	// What the user asked for themselves is theirs: an administrator does not download it.
	code, mine := f.as(t, f.ada, "POST", exportsPath, "")
	if code != http.StatusAccepted {
		t.Fatalf("ada asks: %d %v", code, mine)
	}
	for i := 0; i < 3; i++ {
		if !f.w.RunOnce(ctx) {
			break
		}
	}
	if rec, out := f.doH(t, "GET", user+"/exports/"+mine["id"].(string)+"/download", "", bearer(f.key)); rec.Code != http.StatusForbidden {
		t.Errorf("an administrator downloads the user's own: %d %v", rec.Code, out)
	}
	// Expired ones are gone, and an unknown one is not found.
	*f.clock = f.clock.Add(30 * 24 * time.Hour)
	if rec, _ := f.doH(t, "GET", user+"/exports/"+silentID+"/download", "", bearer(f.key)); rec.Code != http.StatusGone {
		t.Errorf("an expired export: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", user+"/exports/nosuch", "", bearer(f.key)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}

	// An erased user has nothing left to export.
	carl, err := f.svc.Find(ctx, "carl@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Store.EraseUser(ctx, carl.ID, "erased-"+carl.ID+"@erased.invalid", f.svc.Clock()); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.doH(t, "POST", admin+"/users/"+carl.ID+"/export", "", bearer(f.key)); rec.Code != http.StatusConflict {
		t.Errorf("an erased user: %d", rec.Code)
	}
}
