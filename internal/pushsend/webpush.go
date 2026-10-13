package pushsend

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/push"
)

// webSender delivers to browsers with the Web Push protocol: a VAPID-signed request carrying
// the payload encrypted for the subscription (RFC 8291, aes128gcm).
type webSender struct {
	cfg *Config
}

type subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *webSender) Send(ctx context.Context, req push.Request) ([]push.DeviceResult, error) {
	c := req.Config
	key, err := vapidKeyFromBase64(strings.TrimSpace(c["private_key"]))
	if err != nil {
		return nil, fmt.Errorf("%v: %w", err, errCredentials)
	}
	if c["subject"] == "" || c["public_key"] == "" {
		return nil, fmt.Errorf("the VAPID subject and public key are required: %w", errCredentials)
	}
	allowed := strings.Split(c["allowed_hosts"], ",")
	// The hosts this unit may reach: the endpoints of its devices, when the realm allows them.
	var hosts []string
	subs := make([]*subscription, len(req.Deliveries))
	for i, d := range req.Deliveries {
		var sub subscription
		if json.Unmarshal([]byte(d.Token), &sub) != nil {
			continue
		}
		u, err := url.Parse(sub.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (!s.cfg.AllowAddr && u.Port() != "" && u.Port() != "443") || !hostAllowed(u.Hostname(), allowed) {
			continue // answered as failed below: never called
		}
		subs[i] = &sub
		hosts = append(hosts, u.Host)
	}
	timeout := 60 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	client, done, err := s.cfg.httpFor(hosts, timeout)
	if err != nil {
		return nil, err
	}
	defer done()
	index := map[string]int{}
	for i, d := range req.Deliveries {
		index[d.DeviceID] = i
	}
	return eachDelivery(ctx, req.Deliveries, req.Parallelism, func(ctx context.Context, d push.Delivery) (push.DeviceResult, error) {
		sub := subs[index[d.DeviceID]]
		if sub == nil {
			return push.DeviceResult{DeviceID: d.DeviceID, Status: push.StatusFailed, Detail: "the subscription's endpoint is not one the realm allows"}, nil
		}
		payload, err := push.Shape(push.Target{Platform: "web", Provider: push.ProviderWebPush}, d.Message)
		if err != nil {
			return push.DeviceResult{DeviceID: d.DeviceID, Status: push.StatusFailed, Detail: err.Error()}, nil
		}
		body, err := encryptPayload(payload, sub.Keys.P256dh, sub.Keys.Auth)
		if err != nil {
			return push.DeviceResult{DeviceID: d.DeviceID, Status: push.StatusFailed, Detail: "the subscription's keys are not valid"}, nil
		}
		u, _ := url.Parse(sub.Endpoint)
		jwt, err := signJWT(key, map[string]any{"typ": "JWT", "alg": "ES256"}, map[string]any{
			"aud": u.Scheme + "://" + u.Host, "exp": s.cfg.now().Add(12 * time.Hour).Unix(), "sub": c["subject"],
		})
		if err != nil {
			return push.DeviceResult{}, err
		}
		hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
		hr.Header.Set("Authorization", "vapid t="+jwt+", k="+c["public_key"])
		hr.Header.Set("Content-Encoding", "aes128gcm")
		hr.Header.Set("Content-Type", "application/octet-stream")
		hr.Header.Set("TTL", strconv.FormatInt(int64(d.Message.TTLOrDefault().Seconds()), 10))
		hr.Header.Set("Urgency", map[bool]string{true: "high", false: "normal"}[d.Message.Priority == push.PriorityHigh])
		if d.Message.Group != "" {
			hr.Header.Set("Topic", d.Message.Group)
		}
		res, err := client.Do(hr)
		if err != nil {
			return push.DeviceResult{}, scrub(err)
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<14))
		return push.DeviceResult{DeviceID: d.DeviceID, Status: webStatus(res.StatusCode), Detail: fmt.Sprintf("webpush %d", res.StatusCode)}, nil
	})
}

// webStatus maps a push service's answer: 201 delivered, 404 and 410 a subscription that is
// gone, 413 a payload too large, 429 and 5xx worth another try.
func webStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return push.StatusSent
	case status == http.StatusNotFound || status == http.StatusGone:
		return push.StatusInvalid
	case transient(status):
		return push.StatusRetry
	}
	return push.StatusFailed
}

// hostAllowed is the realm's endpoint_hosts check: an exact name, or *.example.com.
func hostAllowed(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if rest, ok := strings.CutPrefix(p, "*."); ok {
			if strings.HasSuffix(host, "."+rest) && len(host) > len(rest)+1 {
				return true
			}
			continue
		}
		if p != "" && host == p {
			return true
		}
	}
	return false
}

// encryptPayload is RFC 8291's message encryption for Web Push (aes128gcm, RFC 8188): the
// body is the header (salt, record size, our public key) and one record holding the payload.
func encryptPayload(plaintext []byte, p256dh, authSecret string) ([]byte, error) {
	uaPub, err := b64.DecodeString(strings.TrimRight(p256dh, "="))
	if err != nil {
		return nil, err
	}
	auth, err := b64.DecodeString(strings.TrimRight(authSecret, "="))
	if err != nil || len(auth) != 16 {
		return nil, errors.New("the auth secret must be 16 bytes")
	}
	curve := ecdh.P256()
	peer, err := curve.NewPublicKey(uaPub)
	if err != nil {
		return nil, err
	}
	asKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPub := asKey.PublicKey().Bytes()
	shared, err := asKey.ECDH(peer)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	// Input keying material: HKDF(salt = auth secret, ikm = ECDH, info = "WebPush: info" 0 ua as).
	prkKey := hmacSHA256(auth, shared)
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPub...), asPub...)
	ikm := hmacSHA256(prkKey, append(keyInfo, 0x01))
	// Content encryption key and nonce from the salt.
	prk := hmacSHA256(salt, ikm)
	cek := hmacSHA256(prk, append([]byte("Content-Encoding: aes128gcm\x00"), 0x01))[:16]
	nonce := hmacSHA256(prk, append([]byte("Content-Encoding: nonce\x00"), 0x01))[:12]
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record, the last one: the data and a 0x02 delimiter.
	record := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 0x02), nil)
	out := make([]byte, 0, 16+4+1+len(asPub)+len(record))
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, 4096)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	return append(out, record...), nil
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}
