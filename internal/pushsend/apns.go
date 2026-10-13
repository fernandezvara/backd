package pushsend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/push"
)

const (
	apnsDefaultBase    = "https://api.push.apple.com"
	apnsDefaultSandbox = "https://api.sandbox.push.apple.com"
	// apnsJWTLife is how long a provider token is reused: Apple refuses tokens refreshed more
	// often than every 20 minutes and ones older than an hour.
	apnsJWTLife = 50 * time.Minute
)

// apnsSender delivers to iOS over HTTP/2 with token authentication (a .p8 key).
type apnsSender struct {
	cfg  *Config
	mu   sync.Mutex
	jwts map[string]cachedToken
}

func (s *apnsSender) Send(ctx context.Context, req push.Request) ([]push.DeviceResult, error) {
	c := req.Config
	base := apnsDefaultBase
	if c["sandbox"] == "true" {
		base = apnsDefaultSandbox
	}
	if s.cfg.APNsBase != "" {
		base = s.cfg.APNsBase
	}
	if c["sandbox"] == "true" && s.cfg.APNsSand != "" {
		base = s.cfg.APNsSand
	}
	if c["key"] == "" || c["key_id"] == "" || c["team_id"] == "" || c["topic"] == "" {
		return nil, fmt.Errorf("the APNs key, key_id, team_id and topic are required: %w", errCredentials)
	}
	bearer, err := s.providerToken(c)
	if err != nil {
		return nil, err
	}
	timeout := 60 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	client, done, err := s.cfg.httpFor([]string{hostOf(base)}, timeout)
	if err != nil {
		return nil, err
	}
	defer done()
	target := push.Target{Platform: "ios", Provider: push.ProviderAPNs}
	return eachDelivery(ctx, req.Deliveries, req.Parallelism, func(ctx context.Context, d push.Delivery) (push.DeviceResult, error) {
		body, err := push.Shape(target, d.Message)
		if err != nil {
			return push.DeviceResult{DeviceID: d.DeviceID, Status: push.StatusFailed, Detail: err.Error()}, nil
		}
		hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+url.PathEscape(d.Token), bytes.NewReader(body))
		hr.Header.Set("Authorization", "bearer "+bearer)
		hr.Header.Set("apns-topic", c["topic"])
		hr.Header.Set("apns-push-type", map[bool]string{true: "background", false: "alert"}[d.Message.Silent])
		priority := "5"
		if d.Message.Priority == push.PriorityHigh && !d.Message.Silent {
			priority = "10"
		}
		hr.Header.Set("apns-priority", priority)
		if ttl := d.Message.TTLOrDefault(); ttl == 0 {
			hr.Header.Set("apns-expiration", "0")
		} else {
			hr.Header.Set("apns-expiration", strconv.FormatInt(s.cfg.now().Add(ttl).Unix(), 10))
		}
		if d.Message.Group != "" {
			hr.Header.Set("apns-collapse-id", d.Message.Group)
		}
		res, err := client.Do(hr)
		if err != nil {
			return push.DeviceResult{}, scrub(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(io.LimitReader(res.Body, 1<<14))
		status, ferr := apnsStatus(res.StatusCode, out)
		return push.DeviceResult{DeviceID: d.DeviceID, Status: status, Detail: fmt.Sprintf("apns %d", res.StatusCode)}, ferr
	})
}

// apnsStatus maps APNs' answer: 410 and BadDeviceToken mean the token is dead; a refused
// provider token fails the unit as a whole; 429 and 5xx are worth another try.
func apnsStatus(status int, body []byte) (string, error) {
	var e struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &e)
	switch {
	case status == http.StatusOK:
		return push.StatusSent, nil
	case status == http.StatusGone || e.Reason == "Unregistered" || e.Reason == "BadDeviceToken":
		return push.StatusInvalid, nil
	case status == http.StatusForbidden && (e.Reason == "InvalidProviderToken" || e.Reason == "ExpiredProviderToken" || e.Reason == "MissingProviderToken"):
		return push.StatusFailed, errCredentials
	case transient(status):
		return push.StatusRetry, nil
	}
	return push.StatusFailed, nil
}

// providerToken returns the ES256 JWT for the key, reused for apnsJWTLife.
func (s *apnsSender) providerToken(c map[string]string) (string, error) {
	sum := sha256.Sum256([]byte(c["key"] + "\x00" + c["key_id"] + "\x00" + c["team_id"]))
	cacheKey := hex.EncodeToString(sum[:])
	now := s.cfg.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.jwts[cacheKey]; ok && now.Before(t.expires) {
		return t.value, nil
	}
	signer, err := privateKeyFromPEM(c["key"])
	if err != nil {
		return "", fmt.Errorf("the APNs key: %v: %w", err, errCredentials)
	}
	jwt, err := signJWT(signer, map[string]any{"alg": "ES256", "kid": c["key_id"]}, map[string]any{"iss": c["team_id"], "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	s.jwts[cacheKey] = cachedToken{value: jwt, expires: now.Add(apnsJWTLife)}
	return jwt, nil
}
