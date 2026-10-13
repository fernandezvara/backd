package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/push"
	"github.com/fernandezvara/backd/internal/registry"
)

// fakeSender answers by the device's token prefix, like the capture provider will.
type fakeSender struct {
	mu       sync.Mutex
	got      []push.Delivery
	attempts map[string]int
}

func (s *fakeSender) Send(_ context.Context, req push.Request) ([]push.DeviceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts == nil {
		s.attempts = map[string]int{}
	}
	var out []push.DeviceResult
	for _, d := range req.Deliveries {
		s.got = append(s.got, d)
		s.attempts[d.Token]++
		status := push.StatusSent
		switch {
		case strings.HasPrefix(d.Token, "test-invalid-"):
			status = push.StatusInvalid
		case strings.HasPrefix(d.Token, "test-fail-"):
			status = push.StatusFailed
		case strings.HasPrefix(d.Token, "test-retry-always-"):
			status = push.StatusRetry
		case strings.HasPrefix(d.Token, "test-retry-") && s.attempts[d.Token] == 1:
			status = push.StatusRetry
		}
		out = append(out, push.DeviceResult{DeviceID: d.DeviceID, Status: status})
	}
	return out, nil
}

func (s *fakeSender) delivered() []push.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]push.Delivery(nil), s.got...)
}

type pushSendFixture struct {
	*rulesFixture
	sender *fakeSender
	worker *Worker
}

// newPushSendFixture is a realm with push, two languages, a template and a function that
// declared `push`, with a worker that delivers through the fake sender.
func newPushSendFixture(t *testing.T) *pushSendFixture {
	t.Helper()
	f := pushFixture(t)
	f.svc.Settings.DefaultLocale, f.svc.Settings.Locales = "en", []string{"en", "es"}
	f.svc.Settings.Push.Platforms = map[string]registry.PushPlatform{
		"android": {Provider: "fcm", Credentials: "FCM"}, "web": {Provider: "webpush", PrivateKey: "VAPID", Subject: "SUBJECT"},
	}
	f.svc.Settings.Push.Limits = registry.PushLimits{PerMinute: 3, PerDay: 24}
	f.svc.Settings.Push.Delivery = registry.PushDelivery{Timeout: time.Minute, RetryAttempts: 3, RetryBackoff: 30 * time.Second, Parallelism: 4}
	f.svc.Settings.Push.SendRetention = 30 * 24 * time.Hour
	rl := f.reg.Realms["acme"]
	rl.Settings.Push = f.svc.Settings.Push
	dir := t.TempDir()
	write := func(rel, content string) {
		writeTestFile(t, filepath.Join(dir, rel), content)
	}
	write("order-shipped/en.yaml", "title: \"Order {{.Data.order_no}} shipped\"\nbody: \"On its way\"\nlink: \"acme://orders/{{.Data.order_no}}\"\n")
	write("order-shipped/es.yaml", "title: \"Pedido {{.Data.order_no}} enviado\"\nbody: \"De camino\"\n")
	tpl, errs := push.Load(dir, []string{"en", "es"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	rl.Push = tpl
	f.reg.Realms["acme"].Databases["app"].Functions.Functions["notify"].Push = &registry.FunctionPush{Audience: true}
	// The providers' secrets are set.
	ctx := context.Background()
	for _, n := range []string{"FCM", "VAPID", "SUBJECT"} {
		if err := f.svc.SetSecret(ctx, "", n, "value-of-"+n, "test"); err != nil {
			t.Fatal(err)
		}
	}
	sender := &fakeSender{}
	f.svc.Settings.Push.Platforms = rl.Settings.Push.Platforms
	w := NewWorker(Config{
		Registry: f.reg, Store: f.store, Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return f.svc
			}
			return nil
		},
		Functions: f.runner, CallbackURL: "http://backd-internal:8081", CallbackKey: f.callbackKey,
		Now: func() time.Time { return *f.clock }, PushSenders: map[string]push.Sender{"fcm": sender, "webpush": sender},
	}, "push-worker")
	return &pushSendFixture{rulesFixture: f, sender: sender, worker: w}
}

// send calls ctx.push.send as app/notify.
func (f *pushSendFixture) send(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	tok := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/notify", Inv: "inv-1", Push: true, Expires: f.clock.Add(time.Minute)})
	req := httptest.NewRequest("POST", "/v1/acme/_push/send", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.internal.ServeHTTP(rec, req)
	return rec.Code, decodeBody(rec)
}

// drain runs the worker until no job is left to claim.
func (f *pushSendFixture) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 10000; i++ {
		if !f.worker.RunOnce(context.Background()) {
			return
		}
	}
	t.Fatal("the worker never ran out of jobs")
}

func (f *pushSendFixture) register(t *testing.T, token, tok string) {
	t.Helper()
	rec, out := f.doH(t, "POST", devices, `{"platform": "android", "token": "`+tok+`"}`, jsonHdr(token))
	if rec.Code != 200 {
		t.Fatalf("register %s: %d %v", tok, rec.Code, out)
	}
}

func (f *pushSendFixture) record(t *testing.T, id string) auth.PushSend {
	t.Helper()
	rec, err := f.svc.PushSendByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestPushReachesAUsersDevicesInTheirLanguage(t *testing.T) {
	f := newPushSendFixture(t)
	ctx := context.Background()
	if _, err := f.svc.SetLocale(ctx, f.adaID, "es"); err != nil {
		t.Fatal(err)
	}
	f.register(t, f.ada, "phone-token")
	f.register(t, f.ada, "tablet-token")
	code, out := f.send(t, `{"user": "`+f.adaID+`", "kind": "order-shipped", "data": {"order_no": "A-17"}, "group": "order-17"}`)
	if code != 202 || out["users"] != float64(1) || out["queued"] != float64(1) || out["limited"] != float64(0) || out["no_devices"] != float64(0) {
		t.Fatalf("send: %d %v", code, out)
	}
	f.drain(t)
	got := f.sender.delivered()
	if len(got) != 2 {
		t.Fatalf("deliveries: %+v", got)
	}
	for _, d := range got {
		if d.Message.Title != "Pedido A-17 enviado" || d.Message.Link != "" || d.Message.Group != "order-17" || d.Message.Kind != "order-shipped" {
			t.Errorf("message: %+v", d.Message)
		}
	}
	rec := f.record(t, out["send_id"].(string))
	if rec.Sent != 2 || rec.Queued != 1 || rec.CompletedAt.IsZero() || rec.Units != 1 || rec.UnitsDone != 1 {
		t.Errorf("record: %+v", rec)
	}
	// The record holds counts, never a title, a body or a token.
	if s := fmt.Sprintf("%+v", rec); strings.Contains(s, "Pedido") || strings.Contains(s, "phone-token") {
		t.Errorf("the record holds content: %s", s)
	}
}

func TestPushRulesOfTheCall(t *testing.T) {
	f := newPushSendFixture(t)
	for name, c := range map[string]struct {
		body, code string
		status     int
	}{
		"nobody":       {`{"kind": "order-shipped"}`, "invalid_push_message", 400},
		"two ways":     {`{"user": "a", "users": ["b"], "kind": "order-shipped"}`, "invalid_push_message", 400},
		"unknown kind": {`{"user": "a", "kind": "nope"}`, "unknown_push_kind", 400},
		"kind + text":  {`{"user": "a", "kind": "order-shipped", "title": "x"}`, "invalid_push_message", 400},
		"raw no body":  {`{"user": "a", "title": "x"}`, "invalid_push_message", 400},
		"data type":    {`{"user": "a", "title": "x", "body": "y", "data": {"n": 1}}`, "invalid_push_message", 400},
		"too big":      {`{"user": "a", "title": "x", "body": "` + strings.Repeat("y", 4200) + `"}`, "payload_too_large", 413},
		"bad audience": {`{"audience": {"admin": true}, "title": "x", "body": "y"}`, "invalid_push_message", 400},
		"empty aud":    {`{"audience": {}, "title": "x", "body": "y"}`, "invalid_push_message", 400},
		"unknown key":  {`{"user": "a", "title": "x", "body": "y", "email": "z"}`, "invalid_json", 400},
	} {
		code, out := f.send(t, c.body)
		if code != c.status || errCode(out) != c.code {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// A function that didn't declare push, or declared other kinds only, or no audience.
	tok := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/notify", Expires: f.clock.Add(time.Minute)})
	req := httptest.NewRequest("POST", "/v1/acme/_push/send", strings.NewReader(`{"user": "a", "title": "x", "body": "y"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.internal.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("not declared: %d", rec.Code)
	}
	f.reg.Realms["acme"].Databases["app"].Functions.Functions["notify"].Push = &registry.FunctionPush{Restricted: true, Kinds: []string{"other"}}
	if code, out := f.send(t, `{"user": "a", "kind": "order-shipped"}`); code != 400 || errCode(out) != "unknown_push_kind" {
		t.Errorf("a kind it didn't declare: %d %v", code, out)
	}
	if code, out := f.send(t, `{"audience": "all", "title": "x", "body": "y"}`); code != 403 || errCode(out) != "push_audience_not_declared" {
		t.Errorf("audience: %d %v", code, out)
	}
	// A provider secret that is not set refuses the send up front.
	f.reg.Realms["acme"].Databases["app"].Functions.Functions["notify"].Push = &registry.FunctionPush{}
	if err := f.svc.DeleteSecret(context.Background(), "", "FCM"); err != nil {
		t.Fatal(err)
	}
	if code, out := f.send(t, `{"user": "a", "title": "x", "body": "y"}`); code != 503 || errCode(out) != "push_unavailable" {
		t.Errorf("missing secret: %d %v", code, out)
	}
}

func TestPushLimitsPruningAndRetries(t *testing.T) {
	f := newPushSendFixture(t)
	ctx := context.Background()
	f.register(t, f.ada, "test-invalid-1")
	f.register(t, f.bob, "test-retry-1")
	f.register(t, f.bob, "test-fail-1")
	sendTo := func(id string) map[string]any {
		t.Helper()
		code, out := f.send(t, `{"user": "`+id+`", "title": "Hi", "body": "There"}`)
		if code != 202 {
			t.Fatalf("send: %d %v", code, out)
		}
		return out
	}
	// A dead token removes the device.
	one := sendTo(f.adaID)
	f.drain(t)
	if rec := f.record(t, one["send_id"].(string)); rec.Invalid != 1 || rec.Sent != 0 || rec.CompletedAt.IsZero() {
		t.Errorf("invalid: %+v", rec)
	}
	if left, _ := f.svc.Store.ListDevices(ctx, f.adaID); len(left) != 0 {
		t.Errorf("the dead device stayed: %+v", left)
	}
	// A retry is sent again, alone, after the backoff; a failure is kept.
	two := sendTo(f.bobID)
	f.drain(t)
	rec := f.record(t, two["send_id"].(string))
	if rec.Failed != 1 || rec.Sent != 0 || !rec.CompletedAt.IsZero() {
		t.Fatalf("after the first attempt: %+v", rec)
	}
	f.svc.Now = func() time.Time { return time.Now().Add(time.Minute) } // past the backoff
	f.drain(t)
	rec = f.record(t, two["send_id"].(string))
	if rec.Failed != 1 || rec.Sent != 1 || rec.CompletedAt.IsZero() {
		t.Fatalf("after the retry: %+v", rec)
	}
	attempts := 0
	for _, d := range f.sender.delivered() {
		if d.Token == "test-retry-1" {
			attempts++
		}
	}
	if attempts != 2 {
		t.Errorf("the retry device was tried %d times", attempts)
	}
	// Three a minute, then limited: counted, and the user is not pushed.
	carl, err := f.svc.Find(ctx, "carl@example.com")
	if err != nil {
		t.Fatal(err)
	}
	f.register(t, f.carl, "carl-token")
	var limited float64
	for i := 0; i < 5; i++ {
		out := sendTo(carl.ID)
		if v, _ := out["limited"].(float64); v > 0 {
			limited++
		}
	}
	f.drain(t)
	if limited != 2 {
		t.Errorf("limited sends: %v", limited)
	}
	n := 0
	for _, d := range f.sender.delivered() {
		if d.Token == "carl-token" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("carl got %d pushes, want 3", n)
	}
}

func TestPushSilentIsNotSentToBrowsers(t *testing.T) {
	f := newPushSendFixture(t)
	f.register(t, f.ada, "phone")
	rec, out := f.doH(t, "POST", devices, `{"platform": "web", "token": "{\"endpoint\":\"https://fcm.googleapis.com/x\",\"keys\":{\"p256dh\":\"AAAA\",\"auth\":\"BBBB\"}}"}`, jsonHdr(f.bob))
	if rec.Code != 200 {
		t.Fatalf("web: %d %v", rec.Code, out)
	}
	code, out := f.send(t, `{"users": ["`+f.adaID+`", "`+f.bobID+`"], "silent": true, "data": {"sync": "1"}}`)
	if code != 202 || out["queued"] != float64(1) || out["no_devices"] != float64(1) {
		t.Fatalf("silent: %d %v", code, out)
	}
	f.drain(t)
	got := f.sender.delivered()
	if len(got) != 1 || got[0].Token != "phone" || !got[0].Message.Silent {
		t.Errorf("deliveries: %+v", got)
	}
}

func TestPushAudienceIsPagedIntoBatches(t *testing.T) {
	f := newPushSendFixture(t)
	ctx := context.Background()
	f.svc.Settings.Push.Limits.PerMinute = 100
	now := time.Now().UTC()
	// 2,500 members with a device each, and the three users of the fixture who have none.
	for i := 0; i < 2500; i++ {
		id := fmt.Sprintf("m%05d", i)
		u := auth.User{ID: id, Email: id + "@example.com", Roles: []string{"member"}, Locale: "en", EmailVerified: true, CreatedAt: now, UpdatedAt: now}
		if err := f.svc.Store.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		sess := auth.Session{ID: "s-" + id, UserID: id, TokenHash: "h-" + id, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := f.svc.Store.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.svc.Store.PutDevice(ctx, auth.Device{ID: "d-" + id, UserID: id, SessionID: sess.ID, Platform: "android", TokenHash: "th-" + id, Token: "x", CreatedAt: now, LastSeen: now}); err != nil {
			t.Fatal(err)
		}
	}
	code, out := f.send(t, `{"audience": {"roles": ["member"]}, "title": "News", "body": "Version 2"}`)
	if code != 202 || out["queued"] != nil || out["users"] != nil || out["jobs"] != float64(1) {
		t.Fatalf("send: %d %v", code, out)
	}
	// The tokens are not real: the sealed value fails to open, so every device fails; what the
	// test checks is the paging and the books.
	f.drain(t)
	rec := f.record(t, out["send_id"].(string))
	if !rec.Planned || rec.Batches != 3 || rec.BatchesDone != 3 || rec.Units != 25 || rec.UnitsDone != 25 || rec.Queued != 2500 || rec.CompletedAt.IsZero() {
		t.Errorf("record: %+v", rec)
	}
	if rec.Sent+rec.Failed+rec.Invalid != 2500 {
		t.Errorf("devices accounted for: %+v", rec)
	}
}

// A batch that runs again (its worker died, its lease expired) makes nothing twice and
// charges nobody twice.
func TestPushBatchThatRunsAgain(t *testing.T) {
	f := newPushSendFixture(t)
	ctx := context.Background()
	f.register(t, f.ada, "ada-phone")
	msgs, err := push.Prepare(f.reg.Realms["acme"].Push, "acme", []string{"en", "es"}, nil, "", push.Message{Title: "Hi", Body: "There"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Store.CreatePushSend(ctx, auth.PushSend{ID: "ps_x", Kind: "raw", Sender: "test", Users: 1, Planned: true, Batches: 1, CreatedAt: *f.clock, ExpiresAt: f.clock.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	first, err := f.svc.PlanPushBatch(ctx, "ps_x-b0", "ps_x", []string{f.adaID}, msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.PlanPushBatch(ctx, "ps_x-b0", "ps_x", []string{f.adaID}, msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Queued != 1 || first.Units != 1 {
		t.Errorf("counts: %+v then %+v", first, second)
	}
	jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Limit: 50})
	units := 0
	for _, j := range jobs {
		if j.Push != nil && j.Push.Kind == auth.PushDeliver {
			units++
		}
	}
	if units != 1 {
		t.Errorf("units made: %d", units)
	}
	// Charged once: the user's minute counter shows one push.
	for i := 0; i < 2; i++ { // two more are allowed (limit 3), the fourth is not
		if err := f.svc.RateLimit(ctx, "push:user:"+f.adaID+":m", 3, time.Minute); err != nil {
			t.Fatalf("the counter was charged twice: %v", err)
		}
	}
	rec := f.record(t, "ps_x")
	if rec.BatchesDone != 1 || rec.Queued != 1 || rec.Units != 1 {
		t.Errorf("record counted twice: %+v", rec)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeBody(rec *httptest.ResponseRecorder) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestPushTestSendAndRecords(t *testing.T) {
	f := newPushSendFixture(t)
	const admin = "/v1/acme/_admin"
	f.register(t, f.ada, "ada-phone")
	// A test send to one user: audited, counted toward their limits, delivered.
	rec, out := f.doH(t, "POST", admin+"/push/test", `{"user": "`+f.adaID+`", "title": "Test", "body": "Does it ring?", "data": {"k": "v"}}`, jsonHdr(f.key))
	if rec.Code != 202 || out["queued"] != float64(1) || out["send_id"] == nil {
		t.Fatalf("test send: %d %v", rec.Code, out)
	}
	id := out["send_id"].(string)
	f.drain(t)
	got := f.sender.delivered()
	if len(got) != 1 || got[0].Message.Title != "Test" || got[0].Message.Data["k"] != "v" {
		t.Fatalf("delivery: %+v", got)
	}
	// The record shows who sent it and the totals, and nothing of the message.
	rec, out = f.doH(t, "GET", admin+"/push/sends/"+id, "", bearer(f.key))
	if rec.Code != 200 || out["sent"] != float64(1) || out["completed"] != true || out["kind"] != "raw" || !strings.HasPrefix(out["sender"].(string), "admin:") {
		t.Fatalf("record: %d %v", rec.Code, out)
	}
	if body := rec.Body.String(); strings.Contains(body, "Does it ring") || strings.Contains(body, "ada-phone") || strings.Contains(body, f.adaID) {
		t.Errorf("the record holds content: %s", body)
	}
	if got := f.auditActions(t, "push."); len(got) != 1 {
		t.Errorf("audit: %v", got)
	}
	// It counted: three a minute in this fixture, so two more and the next is limited.
	for i := 0; i < 2; i++ {
		_, out = f.doH(t, "POST", admin+"/push/test", `{"user": "`+f.adaID+`", "title": "T", "body": "B"}`, jsonHdr(f.key))
		if out["queued"] != float64(1) {
			t.Fatalf("send %d: %v", i, out)
		}
	}
	_, out = f.doH(t, "POST", admin+"/push/test", `{"user": "`+f.adaID+`", "title": "T", "body": "B"}`, jsonHdr(f.key))
	if out["limited"] != float64(1) || out["queued"] != float64(0) {
		t.Errorf("the test send is not counted: %v", out)
	}
	// The list, with its filters.
	rec, out = f.doH(t, "GET", admin+"/push/sends?kind=raw", "", bearer(f.key))
	items, _ := out["items"].([]any)
	if rec.Code != 200 || len(items) != 4 {
		t.Errorf("list: %d %d", rec.Code, len(items))
	}
	rec, out = f.doH(t, "GET", admin+"/push/sends?function=app/notify", "", bearer(f.key))
	if items, _ = out["items"].([]any); rec.Code != 200 || len(items) != 0 {
		t.Errorf("by function: %d %v", rec.Code, out)
	}
	// Needs the right areas.
	if rec, _ := f.doH(t, "POST", admin+"/push/test", `{"user": "`+f.adaID+`", "title": "T", "body": "B"}`, jsonHdr(f.carl)); rec.Code != http.StatusForbidden {
		t.Errorf("a plain user: %d", rec.Code)
	}
}

// A unit that waits out a retry for hours doesn't reach a device whose session ended meanwhile.
func TestPushNotDeliveredAfterTheSessionEnded(t *testing.T) {
	f := newPushSendFixture(t)
	f.register(t, f.ada, "phone")
	code, out := f.send(t, `{"user": "`+f.adaID+`", "title": "Hi", "body": "There"}`)
	if code != 202 || out["queued"] != float64(1) {
		t.Fatalf("send: %d %v", code, out)
	}
	// Planned; then the session passes its expiry before a worker gets to the unit.
	f.svc.Now = func() time.Time { return time.Now().Add(400 * 24 * time.Hour) }
	f.drain(t)
	if got := f.sender.delivered(); len(got) != 0 {
		t.Errorf("delivered to a device without a live session: %+v", got)
	}
	if rec := f.record(t, out["send_id"].(string)); rec.Failed != 1 || rec.Sent != 0 || rec.CompletedAt.IsZero() {
		t.Errorf("record: %+v", rec)
	}
}
