package pushsend

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/egress"
	"github.com/fernandezvara/backd/internal/push"
)

func pemOf(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func message(title string) push.Message {
	return push.Message{Kind: "raw", Title: title, Body: "Body", Link: "acme://x", Data: map[string]string{"k": "v"}, Group: "g1", Priority: push.PriorityHigh}
}

func results(t *testing.T, rs []push.DeviceResult, err error) map[string]string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rs {
		out[r.DeviceID] = r.Status
	}
	return out
}

// verifyJWT checks an ES256 compact JWT against a public key and returns its claims.
func verifyES256(t *testing.T, jwt string, pub *ecdsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %v", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the JWT does not verify")
	}
	raw, _ := b64.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims
}

func TestFCMSender(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	tokenCalls := 0
	var sent []map[string]any
	var auths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokenCalls++
		mu.Unlock()
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || strings.Count(r.Form.Get("assertion"), ".") != 2 {
			http.Error(w, "bad", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.token", "expires_in": 3600})
	})
	mux.HandleFunc("/v1/projects/acme-app/messages:send", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body["message"])
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		switch body["message"]["token"] {
		case "dead":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`))
		case "bad-token":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"The registration token is not a valid FCM registration token"}}`))
		case "bad-payload":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"Invalid value at message.data"}}`))
		case "busy":
			w.WriteHeader(503)
		case "throttled":
			w.WriteHeader(429)
		default:
			_, _ = w.Write([]byte(`{"name":"projects/acme-app/messages/1"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	account, _ := json.Marshal(serviceAccount{ProjectID: "acme-app", ClientEmail: "svc@acme-app.iam.gserviceaccount.com", PrivateKey: pemOf(t, key), TokenURI: srv.URL + "/token"})
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	senders := Senders(Config{FCMBase: srv.URL, TLS: &tls.Config{RootCAs: roots}, AllowAddr: true})
	req := push.Request{Realm: "acme", Platform: "android", Provider: "fcm", Config: map[string]string{"credentials": string(account)}, Parallelism: 4}
	for _, tok := range []string{"ok", "dead", "bad-token", "bad-payload", "busy", "throttled"} {
		req.Deliveries = append(req.Deliveries, push.Delivery{DeviceID: "d-" + tok, Token: tok, Message: message("Hi")})
	}
	rs, err := senders["fcm"].Send(context.Background(), req)
	got := results(t, rs, err)
	want := map[string]string{"d-ok": "sent", "d-dead": "invalid", "d-bad-token": "invalid", "d-bad-payload": "failed", "d-busy": "retry", "d-throttled": "retry"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
	// What was sent: the token, the notification, the data with the link, Android's block.
	var first map[string]any
	for _, m := range sent {
		if m["token"] == "ok" {
			first = m
		}
	}
	notif, _ := first["notification"].(map[string]any)
	data, _ := first["data"].(map[string]any)
	android, _ := first["android"].(map[string]any)
	if notif["title"] != "Hi" || data["link"] != "acme://x" || data["k"] != "v" || android["priority"] != "HIGH" || android["collapse_key"] != "g1" {
		t.Errorf("message: %v", first)
	}
	for _, a := range auths {
		if a != "Bearer ya29.token" {
			t.Errorf("authorization: %q", a)
		}
	}
	// The access token is kept for the next unit.
	if _, err := senders["fcm"].Send(context.Background(), push.Request{Realm: "acme", Platform: "android", Provider: "fcm", Config: req.Config, Deliveries: req.Deliveries[:1]}); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 {
		t.Errorf("token endpoint called %d times", tokenCalls)
	}
	// Credentials that aren't a service account fail the unit as a whole.
	if _, err := senders["fcm"].Send(context.Background(), push.Request{Platform: "android", Config: map[string]string{"credentials": "nope"}, Deliveries: req.Deliveries[:1]}); err == nil {
		t.Error("garbage credentials accepted")
	}
}

func TestFCMRefusedCredentialsFailTheUnit(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	account, _ := json.Marshal(serviceAccount{ProjectID: "p", ClientEmail: "e@x", PrivateKey: pemOf(t, key), TokenURI: srv.URL + "/token"})
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	s := Senders(Config{FCMBase: srv.URL, TLS: &tls.Config{RootCAs: roots}, AllowAddr: true})["fcm"]
	_, err := s.Send(context.Background(), push.Request{Platform: "android", Config: map[string]string{"credentials": string(account)},
		Deliveries: []push.Delivery{{DeviceID: "d", Token: "t", Message: message("Hi")}}})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a 401 should fail the unit: %v", err)
	}
}

func apnsServer(t *testing.T, onRequest func(r *http.Request) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body := onRequest(r)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestAPNsSender(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	type seen struct {
		proto   int
		path    string
		headers http.Header
		body    map[string]any
	}
	var calls []seen
	srv := apnsServer(t, func(r *http.Request) (int, string) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, seen{r.ProtoMajor, r.URL.Path, r.Header.Clone(), body})
		mu.Unlock()
		switch strings.TrimPrefix(r.URL.Path, "/3/device/") {
		case "gone":
			return 410, `{"reason":"Unregistered"}`
		case "bad":
			return 400, `{"reason":"BadDeviceToken"}`
		case "payload":
			return 400, `{"reason":"PayloadTooLarge"}`
		case "busy":
			return 503, `{"reason":"ServiceUnavailable"}`
		}
		return 200, ""
	})
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	s := Senders(Config{APNsBase: srv.URL, TLS: &tls.Config{RootCAs: roots}, AllowAddr: true})["apns"]
	cfg := map[string]string{"key": pemOf(t, key), "key_id": "AB12CD34EF", "team_id": "T123456789", "topic": "com.acme.app"}
	req := push.Request{Realm: "acme", Platform: "ios", Provider: "apns", Config: cfg, Parallelism: 3}
	badge := 4
	for _, tok := range []string{"ok", "gone", "bad", "payload", "busy"} {
		m := message("Hi")
		m.Badge = &badge
		req.Deliveries = append(req.Deliveries, push.Delivery{DeviceID: "d-" + tok, Token: tok, Message: m})
	}
	rs, err := s.Send(context.Background(), req)
	got := results(t, rs, err)
	want := map[string]string{"d-ok": "sent", "d-gone": "invalid", "d-bad": "invalid", "d-payload": "failed", "d-busy": "retry"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
	var ok seen
	for _, c := range calls {
		if c.path == "/3/device/ok" {
			ok = c
		}
	}
	if ok.proto != 2 {
		t.Errorf("APNs needs HTTP/2, the request was HTTP/%d", ok.proto)
	}
	h := ok.headers
	if h.Get("apns-topic") != "com.acme.app" || h.Get("apns-push-type") != "alert" || h.Get("apns-priority") != "10" || h.Get("apns-collapse-id") != "g1" || h.Get("apns-expiration") == "" {
		t.Errorf("headers: %v", h)
	}
	jwt := strings.TrimPrefix(h.Get("Authorization"), "bearer ")
	claims := verifyES256(t, jwt, &key.PublicKey)
	if claims["iss"] != "T123456789" {
		t.Errorf("claims: %v", claims)
	}
	aps, _ := ok.body["aps"].(map[string]any)
	if alert, _ := aps["alert"].(map[string]any); alert["title"] != "Hi" || aps["badge"] != float64(4) || ok.body["link"] != "acme://x" {
		t.Errorf("body: %v", ok.body)
	}
	// A silent push is a background push at priority 5.
	calls = nil
	_, err = s.Send(context.Background(), push.Request{Platform: "ios", Config: cfg, Deliveries: []push.Delivery{{DeviceID: "s", Token: "ok", Message: push.Message{Silent: true, Data: map[string]string{"a": "b"}}}}})
	if err != nil || len(calls) != 1 || calls[0].headers.Get("apns-push-type") != "background" || calls[0].headers.Get("apns-priority") != "5" {
		t.Errorf("silent: %v %+v", err, calls)
	}
}

func TestAPNsRefusedProviderTokenFailsTheUnit(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := apnsServer(t, func(*http.Request) (int, string) { return 403, `{"reason":"InvalidProviderToken"}` })
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	s := Senders(Config{APNsBase: srv.URL, TLS: &tls.Config{RootCAs: roots}, AllowAddr: true})["apns"]
	_, err := s.Send(context.Background(), push.Request{Platform: "ios", Config: map[string]string{"key": pemOf(t, key), "key_id": "k", "team_id": "t", "topic": "x"},
		Deliveries: []push.Delivery{{DeviceID: "d", Token: "ok", Message: message("Hi")}}})
	if err == nil {
		t.Error("a refused provider token should fail the unit")
	}
}

// HTTP/2 to APNs works through the egress proxy: a CONNECT tunnel carries the TLS and the
// protocol is negotiated end to end.
func TestAPNsThroughTheEgressProxy(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var protos []int
	var mu sync.Mutex
	srv := apnsServer(t, func(r *http.Request) (int, string) {
		mu.Lock()
		protos = append(protos, r.ProtoMajor)
		mu.Unlock()
		return 200, ""
	})
	u, _ := url.Parse(srv.URL)
	egressKey := []byte("0123456789abcdef0123456789abcdef")
	proxy := httptest.NewServer((&egress.Proxy{Key: egressKey, AllowPrivate: []string{u.Host}}).Handler())
	defer proxy.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	s := Senders(Config{Proxy: proxy.URL, EgressKey: egressKey, APNsBase: srv.URL, TLS: &tls.Config{RootCAs: roots}})["apns"]
	cfg := map[string]string{"key": pemOf(t, key), "key_id": "k", "team_id": "t", "topic": "com.acme.app"}
	rs, err := s.Send(context.Background(), push.Request{Platform: "ios", Config: cfg, Deliveries: []push.Delivery{{DeviceID: "d", Token: "ok", Message: message("Hi")}}})
	if got := results(t, rs, err); got["d"] != "sent" {
		t.Fatalf("through the proxy: %v", got)
	}
	if len(protos) != 1 || protos[0] != 2 {
		t.Errorf("protocols through the proxy: %v", protos)
	}
	// A proxy that doesn't know the key refuses the unit's token.
	other := Senders(Config{Proxy: proxy.URL, EgressKey: []byte("another-key-another-key-another!"), APNsBase: srv.URL, TLS: &tls.Config{RootCAs: roots}})["apns"]
	if _, err := other.Send(context.Background(), push.Request{Platform: "ios", Config: cfg, Deliveries: []push.Delivery{{DeviceID: "d", Token: "ok", Message: message("Hi")}}}); err == nil {
		t.Error("a unit with a token the proxy doesn't know got through")
	}
}

// decryptPayload is what a browser does with the body of a push (RFC 8291, aes128gcm).
func decryptPayload(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatal("short body")
	}
	salt := body[:16]
	if rs := binary.BigEndian.Uint32(body[16:20]); rs != 4096 {
		t.Fatalf("record size %d", rs)
	}
	idlen := int(body[20])
	asPub := body[21 : 21+idlen]
	record := body[21+idlen:]
	peer, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := uaPriv.ECDH(peer)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...), asPub...)
	ikm := hmacSHA256(hmacSHA256(authSecret, shared), append(keyInfo, 1))
	prk := hmacSHA256(salt, ikm)
	cek := hmacSHA256(prk, append([]byte("Content-Encoding: aes128gcm\x00"), 1))[:16]
	nonce := hmacSHA256(prk, append([]byte("Content-Encoding: nonce\x00"), 1))[:12]
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, record, nil)
	if err != nil {
		t.Fatalf("the record doesn't decrypt: %v", err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatalf("last record delimiter: %x", plain[len(plain)-1])
	}
	return plain[:len(plain)-1]
}

func TestWebPushSender(t *testing.T) {
	vapid, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	vapidPriv := b64.EncodeToString(vapidScalar(vapid))
	vapidPub, _ := vapid.ECDH()
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	_, _ = rand.Read(authSecret)

	var mu sync.Mutex
	type seen struct {
		headers http.Header
		body    []byte
	}
	var got seen
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/ok") {
			got = seen{r.Header.Clone(), body}
		}
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/gone"):
			w.WriteHeader(410)
		case strings.HasSuffix(r.URL.Path, "/busy"):
			w.WriteHeader(429)
		default:
			w.WriteHeader(201)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	sub := func(path string) string {
		b, _ := json.Marshal(map[string]any{"endpoint": srv.URL + "/send/" + path, "keys": map[string]string{
			"p256dh": b64.EncodeToString(uaPriv.PublicKey().Bytes()), "auth": b64.EncodeToString(authSecret)}})
		return string(b)
	}
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	s := Senders(Config{TLS: &tls.Config{RootCAs: roots}, AllowAddr: true})["webpush"]
	cfg := map[string]string{
		"private_key": vapidPriv, "subject": "mailto:ops@acme.example", "public_key": b64.EncodeToString(vapidPub.Bytes()),
		"allowed_hosts": u.Hostname(),
	}
	req := push.Request{Realm: "acme", Platform: "web", Provider: "webpush", Config: cfg, Parallelism: 2}
	for _, p := range []string{"ok", "gone", "busy"} {
		req.Deliveries = append(req.Deliveries, push.Delivery{DeviceID: "d-" + p, Token: sub(p), Message: message("Hi")})
	}
	req.Deliveries = append(req.Deliveries, push.Delivery{DeviceID: "d-other", Message: message("Hi"),
		Token: `{"endpoint":"https://internal.example/x","keys":{"p256dh":"` + b64.EncodeToString(uaPriv.PublicKey().Bytes()) + `","auth":"` + b64.EncodeToString(authSecret) + `"}}`})
	rs, err := s.Send(context.Background(), req)
	res := results(t, rs, err)
	want := map[string]string{"d-ok": "sent", "d-gone": "invalid", "d-busy": "retry", "d-other": "failed"}
	for id, w := range want {
		if res[id] != w {
			t.Errorf("%s: %q, want %q", id, res[id], w)
		}
	}
	// The browser can read what was sent, and the request is VAPID-signed for its origin.
	plain := decryptPayload(t, got.body, uaPriv, authSecret)
	var pushed map[string]any
	if err := json.Unmarshal(plain, &pushed); err != nil || pushed["title"] != "Hi" || pushed["url"] != "acme://x" {
		t.Errorf("payload: %s %v", plain, err)
	}
	h := got.headers
	if h.Get("Content-Encoding") != "aes128gcm" || h.Get("TTL") != "86400" || h.Get("Urgency") != "high" || h.Get("Topic") != "g1" {
		t.Errorf("headers: %v", h)
	}
	auth := h.Get("Authorization")
	jwt, k, _ := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	claims := verifyES256(t, jwt, &vapid.PublicKey)
	if claims["sub"] != "mailto:ops@acme.example" || claims["aud"] != u.Scheme+"://"+u.Host || k != cfg["public_key"] {
		t.Errorf("vapid: %v k=%s", claims, k)
	}
	// Keys that aren't valid fail the device, not the unit; a missing VAPID key fails the unit.
	rs, err = s.Send(context.Background(), push.Request{Platform: "web", Config: cfg, Deliveries: []push.Delivery{{DeviceID: "k", Message: message("Hi"),
		Token: `{"endpoint":"` + srv.URL + `/send/ok","keys":{"p256dh":"AAAA","auth":"AAAA"}}`}}})
	if got := results(t, rs, err); got["k"] != "failed" {
		t.Errorf("bad keys: %v", got)
	}
	if _, err := s.Send(context.Background(), push.Request{Platform: "web", Config: map[string]string{}, Deliveries: req.Deliveries[:1]}); err == nil {
		t.Error("no VAPID key accepted")
	}
}

func TestDirectModeRefusesPrivateAddresses(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := apnsServer(t, func(*http.Request) (int, string) { return 200, "" })
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	cfg := map[string]string{"key": pemOf(t, key), "key_id": "k", "team_id": "t", "topic": "x"}
	deliver := []push.Delivery{{DeviceID: "d", Token: "ok", Message: message("Hi")}}
	// 127.0.0.1 is private: without AllowAddr, nothing is dialed.
	s := Senders(Config{APNsBase: srv.URL, TLS: &tls.Config{RootCAs: roots}})["apns"]
	if _, err := s.Send(context.Background(), push.Request{Platform: "ios", Config: cfg, Deliveries: deliver}); err == nil {
		t.Error("a private address was dialed")
	}
	// And a host the unit didn't name is refused even when addresses are allowed.
	other := Senders(Config{APNsBase: srv.URL, TLS: &tls.Config{RootCAs: roots}, AllowAddr: true})["apns"]
	if _, err := other.Send(context.Background(), push.Request{Platform: "ios", Config: cfg, Deliveries: deliver}); err != nil {
		t.Errorf("the named host: %v", err)
	}
}

func TestEncryptPayloadsAreIndependent(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	a, err := encryptPayload([]byte("hello"), b64.EncodeToString(ua.PublicKey().Bytes()), b64.EncodeToString(auth))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := encryptPayload([]byte("hello"), b64.EncodeToString(ua.PublicKey().Bytes()), b64.EncodeToString(auth))
	if string(a) == string(b) {
		t.Error("two encryptions of one payload are the same: the salt and key must be fresh")
	}
	if string(decryptPayload(t, a, ua, auth)) != "hello" {
		t.Error("round trip")
	}
	if _, err := encryptPayload([]byte("x"), "AAAA", b64.EncodeToString(auth)); err == nil {
		t.Error("a bad public key accepted")
	}
	if _, err := encryptPayload([]byte("x"), b64.EncodeToString(ua.PublicKey().Bytes()), "AAAA"); err == nil {
		t.Error("a short auth secret accepted")
	}
	_ = time.Second
}

// A transport error never carries a device's token (APNs puts it in the path) or a
// subscription's secret URL: the worker logs these.
func TestErrorsDoNotCarryTokensOrSubscriptionURLs(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	// Nothing listens on this address: every request fails at the transport.
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	base := closed.URL
	closed.Close()
	s := Senders(Config{APNsBase: base, AllowAddr: true})["apns"]
	_, err := s.Send(context.Background(), push.Request{Platform: "ios", Config: map[string]string{"key": pemOf(t, key), "key_id": "k", "team_id": "t", "topic": "x"},
		Deliveries: []push.Delivery{{DeviceID: "d", Token: "SECRET-DEVICE-TOKEN-0123456789", Message: message("Hi")}}})
	if err == nil || strings.Contains(err.Error(), "SECRET-DEVICE-TOKEN") {
		t.Errorf("an APNs transport error: %v", err)
	}
	// What a Web Push request would fail with: the secret path is not in what is kept.
	raw := &url.Error{Op: "Post", URL: "https://fcm.googleapis.com/fcm/send/SECRET-SUBSCRIPTION-PATH", Err: errors.New("connection reset")}
	got := scrub(raw)
	if strings.Contains(got.Error(), "SECRET-SUBSCRIPTION-PATH") || !strings.Contains(got.Error(), "fcm.googleapis.com") || !strings.Contains(got.Error(), "connection reset") {
		t.Errorf("scrub: %v", got)
	}
	if plain := errors.New("x"); scrub(plain) != plain {
		t.Error("an error that is not a transport error changed")
	}
}

// A subscription that points at another port of an allowed host is never called.
func TestWebPushNeverCallsAnotherPort(t *testing.T) {
	called := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL) // 127.0.0.1:<random port>
	vapid, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	s := Senders(Config{TLS: &tls.Config{RootCAs: roots}})["webpush"] // the real rules: standard port only
	sub, _ := json.Marshal(map[string]any{"endpoint": srv.URL + "/x", "keys": map[string]string{
		"p256dh": b64.EncodeToString(ua.PublicKey().Bytes()), "auth": b64.EncodeToString(make([]byte, 16))}})
	rs, err := s.Send(context.Background(), push.Request{Platform: "web", Config: map[string]string{
		"private_key": b64.EncodeToString(vapidScalar(vapid)), "subject": "mailto:a@b.c", "public_key": "x", "allowed_hosts": u.Hostname()},
		Deliveries: []push.Delivery{{DeviceID: "d", Token: string(sub), Message: message("Hi")}}})
	if got := results(t, rs, err); got["d"] != "failed" || called {
		t.Errorf("another port: %v called=%v", got, called)
	}
}

// vapidScalar is the 32 bytes of a P-256 private key, as backd push web-keys stores them.
func vapidScalar(k *ecdsa.PrivateKey) []byte {
	b, err := k.Bytes()
	if err != nil {
		panic(err)
	}
	return b
}
