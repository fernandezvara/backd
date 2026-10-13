package push

import "context"

// The result of one device of a delivery unit.
const (
	// StatusSent: the provider accepted the push.
	StatusSent = "sent"
	// StatusInvalid: the token is dead; the device is removed.
	StatusInvalid = "invalid"
	// StatusRetry: throttled or transient; only these devices are sent again.
	StatusRetry = "retry"
	// StatusFailed: permanent for this push; the device is kept.
	StatusFailed = "failed"
)

// Delivery is one push to one device.
type Delivery struct {
	UserID   string
	DeviceID string
	Token    string
	Message  Message
}

// Request is what a sender is asked to do: deliver to the devices of one platform with the
// realm's configuration for the provider.
type Request struct {
	Realm    string
	SendID   string
	Attempt  int // 1 for the first try of the unit
	Platform string
	Provider string
	// Config is the platform's block of realm.yaml with the secrets it names read: credentials
	// (FCM's service-account JSON), key (APNs' .p8 key), private_key and subject (Web Push),
	// and key_id, team_id, topic, sandbox ("true"), public_key and allowed_hosts (comma
	// separated: where a web subscription may point).
	Config map[string]string
	// Parallelism is how many provider requests may be in flight at once.
	Parallelism int
	Deliveries  []Delivery
}

// DeviceResult is how one device went. Detail is for logs and never contains the token.
type DeviceResult struct {
	DeviceID string
	Status   string
	Detail   string
}

// Sender delivers to one provider. It answers a result per device; an error means the call
// as a whole failed (the network is down, the credentials were refused) and the unit is tried
// again. A device with no result counts as failed.
type Sender interface {
	Send(ctx context.Context, req Request) ([]DeviceResult, error)
}
