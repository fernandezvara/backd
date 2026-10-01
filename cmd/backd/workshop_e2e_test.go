package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// The documentation's cookbook (docs: Functions -> Cookbook) is
// examples/config/workshop. This runs every one of its functions for real,
// on MongoDB with a real executor and a real worker: a sync call as the
// caller, a webhook, a privileged idempotent refund, an async report job, and
// both cron schedules.
func TestWorkshopExample(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("deno not found; skipping (the dockerized test image has it)")
	}
	realm := "t" + xid.New().String()
	root := t.TempDir()
	copyTree(t, "../../examples/config/workshop", filepath.Join(root, realm))

	env := map[string]string{
		"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error", "PASSWORD_HASH_CONCURRENCY": "1",
		"BACKD_SECRETS_KEY": "workshop-secrets-key-0123456789abcdef",
	}
	getenv := func(k string) string { return env[k] }
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, db := range []string{realm + "__main", realm + "___system"} {
			_ = client.Database(db).Drop(ctx)
		}
		_ = client.Disconnect(ctx)
	})

	reg0, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := functions.Build(ctx, reg0, functions.Options{}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"provision"}, getenv, nil, io.Discard, io.Discard); code != 0 {
		t.Fatal("provision failed")
	}
	a, err := setup(ctx, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	const token = "e2e-executor-token-0123456789abcdef"
	ex, err := executor.New(executor.Config{Dir: t.TempDir(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	exSrv := httptest.NewServer(ex.Handler())
	defer exSrv.Close()
	a.cfg.ExecutorURL, a.cfg.ExecutorToken = exSrv.URL, token
	a.cfg.CallbackKey = "e2e-callback-key-0123456789abcdef012"
	internal := httptest.NewServer(a.internalHandler())
	defer internal.Close()
	a.cfg.CallbackURL = internal.URL
	api := httptest.NewServer(a.handler())
	defer api.Close()

	base := "/v1/" + realm
	call := func(method, path, token, body string, headers ...string) (int, map[string]any, string) {
		t.Helper()
		req, _ := http.NewRequest(method, api.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		return res.StatusCode, out, string(data)
	}
	expect := func(want int, method, path, token, body string, headers ...string) map[string]any {
		t.Helper()
		code, out, raw := call(method, path, token, body, headers...)
		if code != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, raw)
		}
		return out
	}
	signup := func(email string) string {
		return expect(201, "POST", base+"/_auth/signup", "", `{"email": "`+email+`", "password": "dev-p4ssw0rd!"}`)["token"].(string)
	}

	users := a.realmUsers()(realm)
	_, adminKey, err := users.CreateAPIKey(ctx, "e2e", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	ada, bob := signup("ada@example.com"), signup("bob@example.com")
	if err := users.AddRole(ctx, "bob@example.com", "staff"); err != nil {
		t.Fatal(err)
	}
	expect(204, "PUT", base+"/_admin/secrets/PAYMENT_WEBHOOK_SECRET", adminKey, `{"value": "whsec_test", "database": "main"}`)

	// Orders, as a customer.
	order := func(amount int) string {
		return expect(201, "POST", base+"/main/orders", ada, `{"item": "widget", "quantity": 1, "amount": `+itoa(amount)+`, "status": "draft"}`)["id"].(string)
	}
	o1, o2, o3, o4 := order(1000), order(500), order(300), order(700)
	expect(403, "POST", base+"/main/orders", ada, `{"item": "widget", "quantity": 1, "amount": 100, "status": "paid"}`) // status is not a customer's to set

	// sync, as the caller: ctx.db obeys the orders' rules.
	out := expect(200, "POST", base+"/main/_func/order_total", ada, `{"order_id": "`+o1+`"}`)
	if out["subtotal"] != float64(1000) || out["tax"] != float64(200) || out["total"] != float64(1200) {
		t.Errorf("order_total: %v", out)
	}
	expect(404, "POST", base+"/main/_func/order_total", bob, `{"order_id": "`+o1+`"}`) // not bob's order
	expect(400, "POST", base+"/main/_func/order_total", ada, `{}`)                     // input schema

	// webhook: the signature, then the same event twice.
	hook := func(eventID, orderID, secret string) (int, string) {
		body := `{"id": "` + eventID + `", "type": "payment.succeeded", "order_id": "` + orderID + `"}`
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		code, _, raw := call("POST", base+"/main/_func/payment_webhook", "", body, "x-signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		return code, raw
	}
	if code, raw := hook("evt_1", o1, "wrong-secret"); code != 400 || raw != "invalid signature" {
		t.Errorf("bad signature: %d %q", code, raw)
	}
	if code, raw := hook("evt_1", o1, "whsec_test"); code != 200 || raw != "ok" {
		t.Errorf("webhook: %d %q", code, raw)
	}
	if code, raw := hook("evt_1", o1, "whsec_test"); code != 200 || raw != "already processed" {
		t.Errorf("repeated webhook: %d %q", code, raw)
	}
	if code, raw := hook("evt_2", o4, "whsec_test"); code != 200 || raw != "ok" {
		t.Errorf("second webhook: %d %q", code, raw)
	}
	if code, raw := hook("evt_3", "no-such-order", "whsec_test"); code != 404 || raw != "unknown order" {
		t.Errorf("unknown order: %d %q", code, raw)
	}
	if got := expect(200, "GET", base+"/main/orders/"+o1, ada, "")["status"]; got != "paid" {
		t.Errorf("order after webhook: %v", got)
	}

	// refund: staff only, idempotent, atomic.
	refund := func(token, orderID string, headers ...string) (int, map[string]any) {
		code, out, _ := call("POST", base+"/main/_func/refund", token, `{"order_id": "`+orderID+`", "reason": "damaged"}`, headers...)
		return code, out
	}
	if code, _ := refund(ada, o1, "Idempotency-Key", "a1"); code != 403 {
		t.Errorf("customer refund: %d", code)
	}
	if code, out := refund(bob, o1); code != 400 {
		t.Errorf("refund without a key: %d %v", code, out)
	}
	code, first := refund(bob, o1, "Idempotency-Key", "k1")
	if code != 200 || first["amount"] != float64(1000) {
		t.Fatalf("refund: %d %v", code, first)
	}
	receiptJob, _ := first["receipt_job"].(string)
	if receiptJob == "" {
		t.Fatalf("refund should queue a receipt with ctx.call: %v", first)
	}
	if code, replay := refund(bob, o1, "Idempotency-Key", "k1"); code != 200 || replay["refund_id"] != first["refund_id"] || replay["receipt_job"] != receiptJob {
		t.Errorf("replayed refund: %d %v", code, replay)
	}
	if code, out := refund(bob, o1, "Idempotency-Key", "k2"); code != 409 || errorCode(out) != "not_refundable" {
		t.Errorf("second refund: %d %v", code, out)
	}
	if code, out := refund(bob, o2, "Idempotency-Key", "k3"); code != 409 || errorCode(out) != "not_refundable" {
		t.Errorf("refund of a draft: %d %v", code, out)
	}
	if got := expect(200, "GET", base+"/main/orders/"+o1, ada, "")["status"]; got != "refunded" {
		t.Errorf("order after refund: %v", got)
	}
	if items := expect(200, "GET", base+"/main/refunds", adminKey, "")["items"].([]any); len(items) != 1 {
		t.Errorf("refunds: %v", items)
	}

	// Internal functions have no HTTP route, for anyone.
	for _, name := range []string{"refund_receipt", "nightly_cleanup"} {
		for _, cred := range []string{"", ada, bob, adminKey} {
			expect(404, "POST", base+"/main/_func/"+name, cred, `{}`)
		}
	}

	// async: a report job, run by a worker, read back through _jobs.
	w := httpapi.NewWorker(a.handlerConfig(), "e2e-worker")
	enq := expect(202, "POST", base+"/main/_func/export_orders", ada, `{}`)
	jobID := enq["id"].(string)
	expect(200, "GET", base+"/main/_jobs/"+jobID, ada, "") // queued, readable by its caller
	for w.RunOnce(ctx) {
	}
	job := expect(200, "GET", base+"/main/_jobs/"+jobID, ada, "")
	result, _ := job["result"].(map[string]any)
	if job["status"] != "done" || result["status"] != "ok" {
		t.Fatalf("export job: %v", job)
	}
	output := result["output"].(map[string]any)
	if output["rows"] != float64(4) || output["reused"] != false {
		t.Errorf("export output: %v", output)
	}
	report := expect(200, "GET", base+"/main/reports/"+output["report_id"].(string), ada, "")
	if csv := report["csv"].(string); !strings.HasPrefix(csv, "id,item,quantity,amount,status\n") || !strings.Contains(csv, o1+",widget,1,1000,refunded") {
		t.Errorf("report csv: %q", csv)
	}
	expect(404, "GET", base+"/main/reports/"+output["report_id"].(string), bob, "")
	if items := expect(200, "GET", base+"/_admin/jobs?function=main/export_orders&status=done", adminKey, "")["items"].([]any); len(items) != 1 {
		t.Errorf("admin job listing: %v", items)
	}

	// The receipt job refund queued ran with the export (same worker loop): it
	// wrote one receipt, as its own admin access, and the history links it to
	// the refund call that queued it.
	receipt := expect(200, "GET", base+"/main/_jobs/"+receiptJob, adminKey, "")
	if r, _ := receipt["result"].(map[string]any); receipt["status"] != "done" || r["status"] != "ok" || r["output"].(map[string]any)["written"] != true {
		t.Fatalf("receipt job: %v", receipt)
	}
	if items := expect(200, "GET", base+"/main/receipts", adminKey, "")["items"].([]any); len(items) != 1 || !strings.Contains(items[0].(map[string]any)["text"].(string), "Refund of 10.00 for order "+o1) {
		t.Errorf("receipts: %v", items)
	}
	expect(403, "GET", base+"/main/receipts", ada, "") // customers can't read receipts
	nested := expect(200, "GET", base+"/_admin/invocations?function=main/refund_receipt", adminKey, "")["items"].([]any)
	if len(nested) != 1 {
		t.Fatalf("receipt invocations: %v", nested)
	}
	if n := nested[0].(map[string]any); n["origin"] != "function" || n["parent_id"] == nil {
		t.Errorf("receipt invocation: %v", n)
	} else {
		found := false
		for _, p := range expect(200, "GET", base+"/_admin/invocations?function=main/refund", adminKey, "")["items"].([]any) {
			if pm := p.(map[string]any); pm["id"] == n["parent_id"] {
				found = pm["origin"] == "http" && pm["status"] == "ok" && pm["request_id"] == n["request_id"]
			}
		}
		if !found {
			t.Errorf("the receipt's parent is not the successful refund call: %v", n)
		}
	}

	// cron. A draft nobody finished for 45 days (backdated in MongoDB) and
	// today's orders, then both schedules on a clock set to tomorrow so the
	// callback credentials stay valid.
	old := time.Now().Add(-45 * 24 * time.Hour)
	if _, err := client.Database(realm+"__main").Collection("orders").UpdateOne(ctx, bson.D{{Key: "_id", Value: o3}}, bson.D{{Key: "$set", Value: bson.D{{Key: "_meta.created_at", Value: old}}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 10, 0, time.UTC)
	tick := func(at time.Time) {
		cfg := a.handlerConfig()
		cfg.Now = func() time.Time { return at }
		cw := httpapi.NewWorker(cfg, "cron-worker")
		cw.EnqueueDue(ctx)
		cw.EnqueueDue(ctx) // a second look at the same minute creates nothing
		for cw.RunOnce(ctx) {
		}
	}
	tick(tomorrow) // 00:00:10: only @daily is due
	tick(tomorrow.Add(3 * time.Hour))
	jobs := expect(200, "GET", base+"/_admin/jobs?scheduled=true", adminKey, "")["items"].([]any)
	if len(jobs) != 2 {
		t.Fatalf("scheduled jobs: %v", jobs)
	}
	byFunction := map[string]map[string]any{}
	for _, j := range jobs {
		jm := j.(map[string]any)
		byFunction[jm["function"].(string)] = jm
		if jm["status"] != "done" || jm["result"].(map[string]any)["status"] != "ok" {
			t.Errorf("scheduled job did not finish ok: %v", jm)
		}
	}
	if byFunction["main/daily_digest"] == nil || byFunction["main/nightly_cleanup"] == nil {
		t.Fatalf("scheduled functions: %v", byFunction)
	}
	wantID := auth.ScheduledJobID("main", "nightly_cleanup", time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 3, 0, 0, 0, time.UTC))
	if byFunction["main/nightly_cleanup"]["id"] != wantID {
		t.Errorf("cron job id = %v, want %s", byFunction["main/nightly_cleanup"]["id"], wantID)
	}
	// An administrator runs it by hand through the admin API (it is
	// internal, so it has no _func route): it runs as its schedule would, with
	// ctx.admin, and behaves the same: the draft is already gone.
	byHand := expect(202, "POST", base+"/_admin/functions/main/nightly_cleanup/invoke", adminKey, `{}`)
	for w.RunOnce(ctx) {
	}
	if j := expect(200, "GET", base+"/main/_jobs/"+byHand["id"].(string), adminKey, ""); j["status"] != "done" || j["result"].(map[string]any)["status"] != "ok" {
		t.Fatalf("nightly_cleanup run by hand: %v", j)
	}
	if audit := expect(200, "GET", base+"/_admin/audit?action=function.invoke_manual", adminKey, "")["items"].([]any); len(audit) != 1 {
		t.Errorf("audit of the manual run: %v", audit)
	}
	// The scheduled digest summarizes yesterday (nothing was ordered then).
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	digests := expect(200, "GET", base+"/main/digests", adminKey, "")["items"].([]any)
	if len(digests) != 1 || digests[0].(map[string]any)["day"] != yesterday || digests[0].(map[string]any)["orders"] != float64(0) {
		t.Fatalf("scheduled digest: %v", digests)
	}
	// An API key builds today's by hand: o1 (refunded), o2 (draft), o4 (paid, 700); o3 was backdated.
	manual := expect(202, "POST", base+"/main/_func/daily_digest", adminKey, `{"day": "`+now.Format("2006-01-02")+`"}`)
	for w.RunOnce(ctx) {
	}
	if j := expect(200, "GET", base+"/main/_jobs/"+manual["id"].(string), adminKey, ""); j["status"] != "done" {
		t.Fatalf("manual digest job: %v", j)
	}
	digests = expect(200, "GET", base+"/main/digests?where="+`%7B%22day%22%3A%22`+now.Format("2006-01-02")+`%22%7D`, adminKey, "")["items"].([]any)
	if len(digests) != 1 {
		t.Fatalf("today's digest: %v", digests)
	}
	if d := digests[0].(map[string]any); d["orders"] != float64(3) || d["revenue"] != float64(700) {
		t.Errorf("today's digest: %v", d)
	}
	// Cleanup deleted the 45-day-old draft and kept the fresh ones.
	expect(404, "GET", base+"/main/orders/"+o3, ada, "")
	expect(200, "GET", base+"/main/orders/"+o2, ada, "")
	job = expect(200, "GET", base+"/main/_jobs/"+wantID, adminKey, "")
	if out := job["result"].(map[string]any)["output"].(map[string]any); out["deleted"] != float64(1) {
		t.Errorf("cleanup output: %v", out)
	}
}

func errorCode(out map[string]any) any {
	e, _ := out["error"].(map[string]any)
	return e["code"]
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// copyTree copies a directory, leaving out build output and Deno tests.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if d.IsDir() {
			if d.Name() == ".build" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		if strings.HasSuffix(path, ".test.ts") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
