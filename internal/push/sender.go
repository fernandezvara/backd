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
// realm's credentials for the provider (the values of the secrets the platform names).
type Request struct {
	Realm       string
	SendID      string
	Attempt     int // 1 for the first try of the unit
	Platform    string
	Provider    string
	Credentials map[string]string // secret name → value
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
