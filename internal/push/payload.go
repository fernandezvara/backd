package push

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
)

// The payloads the providers receive. Senders (a later step) wrap them in their request; the
// shapes live here so a message's size is checked, before anything is queued, against what
// will really be sent.

// The providers a platform's pushes can go through.
const (
	ProviderFCM     = "fcm"
	ProviderAPNs    = "apns"
	ProviderWebPush = "webpush"
	ProviderCapture = "capture"
)

// Payload limits, in bytes.
const (
	// FCMLimit and APNsLimit are the 4 KiB of the providers' messages.
	FCMLimit  = 4096
	APNsLimit = 4096
	// WebPushLimit is what RFC 8291 leaves of a 4096-byte record for the plaintext.
	WebPushLimit = 3993
)

// Target is one enabled platform and the provider that delivers to it.
type Target struct{ Platform, Provider string }

// naturalProvider is the provider a platform's payload takes the shape of when the provider is
// `capture`, which stores what a real one would have received.
func (t Target) shape() string {
	if t.Provider != ProviderCapture {
		return t.Provider
	}
	switch t.Platform {
	case "ios":
		return ProviderAPNs
	case "web":
		return ProviderWebPush
	}
	return ProviderFCM
}

// Limit is the most a provider accepts for the target.
func (t Target) Limit() int {
	switch t.shape() {
	case ProviderAPNs:
		return APNsLimit
	case ProviderWebPush:
		return WebPushLimit
	}
	return FCMLimit
}

// TooLargeError says a message doesn't fit a provider.
type TooLargeError struct {
	Platform string
	Size     int
	Limit    int
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("the message is %d bytes for %s; the provider takes at most %d", e.Size, e.Platform, e.Limit)
}

// Shape returns the JSON body the target's provider receives for the message (without the
// device token, which is not part of the payload that counts against the limit).
func Shape(t Target, m Message) ([]byte, error) {
	var body map[string]any
	switch t.shape() {
	case ProviderAPNs:
		body = apnsPayload(m)
		merge(body, m.Platform["ios"])
	case ProviderWebPush:
		body = webPayload(m)
		merge(body, m.Platform["web"])
	default:
		body = fcmPayload(t.Platform, m)
	}
	return json.Marshal(body)
}

// CheckSize shapes the message for every target and refuses it when one is over its limit.
func CheckSize(targets []Target, m Message) error {
	for _, t := range targets {
		b, err := Shape(t, m)
		if err != nil {
			return err
		}
		if len(b) > t.Limit() {
			return &TooLargeError{Platform: t.Platform, Size: len(b), Limit: t.Limit()}
		}
	}
	return nil
}

// dataOf is the message's data with its link, which the app opens.
func dataOf(m Message) map[string]string {
	out := make(map[string]string, len(m.Data)+1)
	maps.Copy(out, m.Data)
	if m.Link != "" {
		out["link"] = m.Link
	}
	return out
}

func seconds(m Message) string { return strconv.FormatInt(int64(m.TTLOrDefault().Seconds()), 10) }

// fcmPayload is the `message` of FCM's HTTP v1 send, with the platform's own block.
func fcmPayload(platform string, m Message) map[string]any {
	msg := map[string]any{}
	if !m.Silent {
		msg["notification"] = map[string]any{"title": m.Title, "body": m.Body}
	}
	if d := dataOf(m); len(d) > 0 {
		msg["data"] = d
	}
	high := m.Priority == PriorityHigh
	switch platform {
	case "ios":
		headers := map[string]any{"apns-expiration": seconds(m), "apns-priority": map[bool]string{true: "10", false: "5"}[high]}
		if m.Group != "" {
			headers["apns-collapse-id"] = m.Group
		}
		aps := map[string]any{}
		if m.Silent {
			aps["content-available"] = 1
			headers["apns-push-type"] = "background"
			headers["apns-priority"] = "5"
		}
		if m.Badge != nil {
			aps["badge"] = *m.Badge
		}
		if m.Sound != "" {
			aps["sound"] = m.Sound
		}
		block := map[string]any{"headers": headers, "payload": map[string]any{"aps": aps}}
		merge(block, m.Platform["ios"])
		msg["apns"] = block
	case "web":
		headers := map[string]any{"TTL": seconds(m), "Urgency": map[bool]string{true: "high", false: "normal"}[high]}
		if m.Group != "" {
			headers["Topic"] = m.Group
		}
		block := map[string]any{"headers": headers}
		if m.Link != "" {
			block["fcm_options"] = map[string]any{"link": m.Link}
		}
		merge(block, m.Platform["web"])
		msg["webpush"] = block
	default:
		block := map[string]any{"ttl": seconds(m) + "s", "priority": map[bool]string{true: "HIGH", false: "NORMAL"}[high]}
		if m.Group != "" {
			block["collapse_key"] = m.Group
		}
		if m.Sound != "" && !m.Silent {
			block["notification"] = map[string]any{"sound": m.Sound}
		}
		merge(block, m.Platform["android"])
		msg["android"] = block
	}
	return map[string]any{"message": msg}
}

// apnsPayload is the body of an APNs request.
func apnsPayload(m Message) map[string]any {
	aps := map[string]any{}
	if m.Silent {
		aps["content-available"] = 1
	} else {
		aps["alert"] = map[string]any{"title": m.Title, "body": m.Body}
	}
	if m.Badge != nil {
		aps["badge"] = *m.Badge
	}
	if m.Sound != "" {
		aps["sound"] = m.Sound
	}
	if m.Group != "" {
		aps["thread-id"] = m.Group
	}
	body := map[string]any{"aps": aps}
	for k, v := range dataOf(m) {
		body[k] = v
	}
	return body
}

// webPayload is what a service worker receives in its push event.
func webPayload(m Message) map[string]any {
	body := map[string]any{"title": m.Title, "body": m.Body}
	if m.Link != "" {
		body["url"] = m.Link
	}
	if len(m.Data) > 0 {
		body["data"] = m.Data
	}
	if m.Group != "" {
		body["tag"] = m.Group
	}
	return body
}

// merge adds src to dst, going into objects both have.
func merge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}
