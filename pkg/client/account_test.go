package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConsents(t *testing.T) {
	state := `{"consents":{"terms":"3"},"to_accept":[{"name":"privacy","version":"2"}]`
	c, r := recClient(t, map[string]string{
		"GET /_consents":              `{"items":[{"name":"terms","required":true,"current":{"version":"3","reaccept":true,"reaccept_by":"2030-01-01"},"locales":["en","es"]}]}`,
		"GET /_consents/terms/3":      `{"name":"terms","version":"3","locale":"es","text":"# Términos","hash":"abc","current":true}`,
		"GET /_auth/consents":         state + `,"history":[{"consent":"terms","version":"3","action":"accepted","method":"signup","locale":"en","at":"x"}]}`,
		"POST /_auth/consents":        state + `}`,
		"DELETE /_auth/consents/mail": state + `}`,
	})
	cs := c.Consents()
	cur, err := cs.Current(bg)
	if err != nil || !cur[0].Required || !cur[0].Current.Reaccept || cur[0].Current.ReacceptBy != "2030-01-01" || cur[0].Locales[1] != "es" {
		t.Fatalf("%v %+v", err, cur)
	}
	txt, err := cs.Text(bg, "terms", "3", "es")
	if err != nil || txt.Text != "# Términos" || !txt.Current || r.log[1] != "GET /_consents/terms/3?locale=es" {
		t.Errorf("%v %+v %s", err, txt, r.log[1])
	}
	st, err := cs.State(bg)
	if err != nil || st.Consents["terms"] != "3" || st.ToAccept[0] != (ConsentRef{"privacy", "2"}) || st.History[0].Action != "accepted" {
		t.Errorf("%v %+v", err, st)
	}
	if s, err := cs.Accept(bg, map[string]string{"privacy": "2"}); err != nil || s.Consents["terms"] != "3" || r.bodies[3] != `{"consents":{"privacy":"2"}}` {
		t.Errorf("%v %+v %s", err, s, r.bodies[3])
	}
	if _, err := cs.Withdraw(bg, "mail"); err != nil {
		t.Error(err)
	}
}

func TestConsentsCurrentAndTextSendNoCredentials(t *testing.T) {
	var auth []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		io.WriteString(w, `{"items":[],"name":"n"}`)
	}, func(o *Options) { o.APIKey = "bdk_x" })
	c.Consents().Current(bg)
	c.Consents().Text(bg, "n", "1", "")
	if auth[0] != "" || auth[1] != "" {
		t.Errorf("sent %q", auth)
	}
}

func TestAccountExportFlow(t *testing.T) {
	var polls atomic.Int32
	c, r := recClient(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/me/export"):
			w.WriteHeader(202)
			fmt.Fprint(w, `{"id":"e1","status":"queued","created_at":"x"}`)
		case req.URL.Path == "/v1/r/_auth/me/exports":
			fmt.Fprint(w, `{"items":[{"id":"e1","status":"running","created_at":"x","step":"files"}]}`)
		case req.URL.Path == "/v1/r/_auth/me/exports/e1":
			if polls.Add(1) < 3 {
				fmt.Fprint(w, `{"id":"e1","status":"running","created_at":"x","step":"files"}`)
				return
			}
			fmt.Fprint(w, `{"id":"e1","status":"ready","created_at":"x","size":4,"counts":{"files":2}}`)
		case req.URL.Path == "/v1/r/_auth/me/exports/e1/download":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"url":"http://%s/stored.zip","expires_at":"y"}`, req.Host)
		case req.URL.Path == "/stored.zip":
			if req.Header.Get("Authorization") != "" {
				t.Error("the storage got backd's credentials")
			}
			fmt.Fprint(w, "ZIP!")
		case req.URL.Path == "/v1/r/_auth/me/exports/e2/download":
			w.Header().Set("Content-Type", "application/zip")
			fmt.Fprint(w, "DIRECT")
		}
	}))
	t.Cleanup(srv.Close)
	_ = r
	c.url = srv.URL
	a := c.Account()

	e, err := a.RequestExport(bg)
	if err != nil || e.ID != "e1" || e.Status != "queued" {
		t.Fatalf("%v %+v", err, e)
	}
	if l, err := a.Exports(bg); err != nil || l[0].Step != "files" {
		t.Errorf("%v %+v", err, l)
	}
	var seen []string
	ready, err := a.WaitForExport(bg, "e1", ExportWaitOptions{PollInterval: time.Millisecond, OnProgress: func(e *DataExport) { seen = append(seen, e.Status) }})
	if err != nil || ready.Size != 4 || ready.Counts["files"] != 2 || strings.Join(seen, ",") != "running,running,ready" {
		t.Errorf("%v %+v %v", err, ready, seen)
	}
	var buf bytes.Buffer
	if n, err := a.DownloadExport(bg, "e1", &buf); err != nil || n != 4 || buf.String() != "ZIP!" {
		t.Errorf("%v %d %q", err, n, buf.String())
	}
	buf.Reset()
	if n, err := a.DownloadExport(bg, "e2", &buf); err != nil || buf.String() != "DIRECT" || n != 6 {
		t.Errorf("direct: %v %d %q", err, n, buf.String())
	}
}

func TestWaitForExportFailures(t *testing.T) {
	for _, status := range []string{"failed", "expired"} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"id":"e","status":%q,"created_at":"x"}`, status)
		})
		if _, err := c.Account().WaitForExport(bg, "e", ExportWaitOptions{PollInterval: time.Millisecond}); err == nil || !strings.Contains(err.Error(), status) {
			t.Errorf("%s: %v", status, err)
		}
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"e","status":"running","created_at":"x"}`)
	})
	if _, err := c.Account().WaitForExport(bg, "e", ExportWaitOptions{PollInterval: 5 * time.Millisecond, Timeout: 12 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("timeout: %v", err)
	}
}

func TestPushDevices(t *testing.T) {
	dev := `{"id":"d1","platform":"ios","name":null,"created_at":"x","last_seen":"y"}`
	c, r := recClient(t, map[string]string{
		"POST /_auth/devices":      dev,
		"GET /_auth/devices":       `{"items":[` + dev + `]}`,
		"DELETE /_auth/devices/d1": "",
	})
	p := c.Push()
	if ok, err := p.Unregister(bg, ""); err != nil || ok {
		t.Errorf("nothing to unregister: %v %v", err, ok)
	}
	d, err := p.Register(bg, DeviceInput{Platform: "ios", Token: "apns-token", Name: "Ada's phone"})
	if err != nil || d.ID != "d1" || d.Name != nil || p.DeviceID() != "d1" {
		t.Fatalf("%v %+v", err, d)
	}
	if r.bodies[0] != `{"platform":"ios","token":"apns-token","name":"Ada's phone"}` {
		t.Errorf("sent %s", r.bodies[0])
	}
	if l, err := p.Devices(bg); err != nil || len(l) != 1 {
		t.Errorf("%v %v", err, l)
	}
	if ok, err := p.Unregister(bg, ""); err != nil || !ok || p.DeviceID() != "" {
		t.Errorf("%v %v %q", err, ok, p.DeviceID())
	}
}

func TestPushForgetsTheDeviceWhenSignedOut(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/devices", "", `{"id":"d1","platform":"ios","name":null,"created_at":"x","last_seen":"y"}`)
	rt.json("POST /_auth/logout", "204", ``)
	c := authClient(t, rt)
	c.Push().Register(bg, DeviceInput{Platform: "ios", Token: "t"})
	c.Auth().Logout(bg)
	if c.Push().DeviceID() != "" {
		t.Error("the device id survived a sign-out")
	}
}

func TestRegisterOnLaunch(t *testing.T) {
	rt := newRoutes()
	rt.json("POST /_auth/login", "", sessionJSON)
	rt.json("POST /_auth/devices", "", `{"id":"d1","platform":"android","name":null,"created_at":"x","last_seen":"y"}`)
	c := authClient(t, rt)
	registered := make(chan string, 4)
	errs := make(chan error, 4)
	stop := c.Push().RegisterOnLaunch(func(ctx context.Context) (*DeviceInput, error) {
		return &DeviceInput{Platform: "android", Token: "fcm"}, nil
	}, func(err error) { errs <- err })
	rt.h["POST /_auth/devices"] = func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		registered <- b["token"].(string)
		io.WriteString(w, `{"id":"d1","platform":"android","name":null,"created_at":"x","last_seen":"y"}`)
	}
	if _, err := c.Auth().Login(bg, SignInInput{}); err != nil {
		t.Fatal(err)
	}
	select {
	case tok := <-registered:
		if tok != "fcm" {
			t.Errorf("token %q", tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the device was not registered after the sign-in")
	}
	stop()
	c.Auth().Login(bg, SignInInput{})
	select {
	case <-registered:
		t.Error("registered after stop")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-errs:
		t.Errorf("unexpected error: %v", err)
	default:
	}
}
