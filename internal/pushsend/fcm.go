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
	"strings"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/push"
)

const (
	fcmDefaultBase = "https://fcm.googleapis.com"
	fcmScope       = "https://www.googleapis.com/auth/firebase.messaging"
	fcmTokenURL    = "https://oauth2.googleapis.com/token"
)

// fcmSender delivers through FCM's HTTP v1 API for Android, for iOS apps on the FCM SDK and
// for browsers using FCM tokens. The OAuth2 access token comes from the service account's key.
type fcmSender struct {
	cfg    *Config
	mu     sync.Mutex
	tokens map[string]cachedToken // by a hash of the service account
}

type serviceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func (s *fcmSender) Send(ctx context.Context, req push.Request) ([]push.DeviceResult, error) {
	var sa serviceAccount
	if err := json.Unmarshal([]byte(req.Config["credentials"]), &sa); err != nil || sa.ProjectID == "" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, fmt.Errorf("the FCM credentials are not a service account's JSON (project_id, client_email, private_key): %w", errCredentials)
	}
	tokenURL := sa.TokenURI
	if tokenURL == "" {
		tokenURL = fcmTokenURL
	}
	if s.cfg.OAuthURL != "" {
		tokenURL = s.cfg.OAuthURL
	}
	base := fcmDefaultBase
	if s.cfg.FCMBase != "" {
		base = s.cfg.FCMBase
	}
	hosts := []string{hostOf(base), hostOf(tokenURL)}
	timeout := 60 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	client, done, err := s.cfg.httpFor(hosts, timeout)
	if err != nil {
		return nil, err
	}
	defer done()
	access, err := s.accessToken(ctx, client, sa, tokenURL)
	if err != nil {
		return nil, err
	}
	send := base + "/v1/projects/" + url.PathEscape(sa.ProjectID) + "/messages:send"
	return eachDelivery(ctx, req.Deliveries, req.Parallelism, func(ctx context.Context, d push.Delivery) (push.DeviceResult, error) {
		payload, err := push.Shape(push.Target{Platform: req.Platform, Provider: push.ProviderFCM}, d.Message)
		if err != nil {
			return push.DeviceResult{DeviceID: d.DeviceID, Status: push.StatusFailed, Detail: err.Error()}, nil
		}
		var body map[string]map[string]any
		_ = json.Unmarshal(payload, &body)
		body["message"]["token"] = d.Token
		raw, _ := json.Marshal(body)
		hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, send, bytes.NewReader(raw))
		hr.Header.Set("Authorization", "Bearer "+access)
		hr.Header.Set("Content-Type", "application/json")
		res, err := client.Do(hr)
		if err != nil {
			return push.DeviceResult{}, err
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		return push.DeviceResult{DeviceID: d.DeviceID, Status: fcmStatus(res.StatusCode, out), Detail: fmt.Sprintf("fcm %d", res.StatusCode)}, fcmFailure(res.StatusCode)
	})
}

// fcmFailure is the error of an answer that fails the unit as a whole: credentials refused.
func fcmFailure(status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return errCredentials
	}
	return nil
}

// fcmStatus maps FCM's answer for a token: UNREGISTERED (404) and an INVALID_ARGUMENT that
// names the registration token mean it is dead; 429 and 5xx are worth another try.
func fcmStatus(status int, body []byte) string {
	switch {
	case status == http.StatusOK:
		return push.StatusSent
	case status == http.StatusNotFound:
		return push.StatusInvalid
	case transient(status):
		return push.StatusRetry
	}
	var e struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	for _, d := range e.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return push.StatusInvalid
		}
	}
	switch {
	case e.Error.Status == "UNREGISTERED":
		return push.StatusInvalid
	case e.Error.Status == "INVALID_ARGUMENT" && strings.Contains(strings.ToLower(e.Error.Message), "registration token"):
		return push.StatusInvalid
	case e.Error.Status == "UNAVAILABLE" || e.Error.Status == "INTERNAL" || e.Error.Status == "QUOTA_EXCEEDED":
		return push.StatusRetry
	}
	return push.StatusFailed
}

// accessToken returns an OAuth2 access token for the service account, cached until a minute
// before it expires.
func (s *fcmSender) accessToken(ctx context.Context, client *http.Client, sa serviceAccount, tokenURL string) (string, error) {
	sum := sha256.Sum256([]byte(sa.ClientEmail + "\x00" + sa.PrivateKey))
	key := hex.EncodeToString(sum[:])
	now := s.cfg.now()
	s.mu.Lock()
	if t, ok := s.tokens[key]; ok && now.Before(t.expires) {
		s.mu.Unlock()
		return t.value, nil
	}
	s.mu.Unlock()
	signer, err := privateKeyFromPEM(sa.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("the service account's private key: %v: %w", err, errCredentials)
	}
	assertion, err := signJWT(signer, map[string]any{"alg": "RS256", "typ": "JWT"}, map[string]any{
		"iss": sa.ClientEmail, "scope": fcmScope, "aud": tokenURL, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(hr)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		return "", fmt.Errorf("google refused the service account (%d): %w", res.StatusCode, errCredentials)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil || out.AccessToken == "" {
		return "", fmt.Errorf("google's token endpoint answered %d", res.StatusCode)
	}
	s.mu.Lock()
	s.tokens[key] = cachedToken{value: out.AccessToken, expires: now.Add(time.Duration(out.ExpiresIn)*time.Second - time.Minute)}
	s.mu.Unlock()
	return out.AccessToken, nil
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host
}
