package client

import (
	"context"
	"net/http"
	"sync"
)

// Device is a device registered to receive push notifications.
type Device struct {
	ID       string  `json:"id"`
	Platform string  `json:"platform"` // "android", "ios" or "web"
	Name     *string `json:"name"`
	// CreatedAt and LastSeen are RFC 3339.
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen"`
}

// DeviceInput is what a device registers with.
type DeviceInput struct {
	// Platform is "android", "ios" or "web".
	Platform string `json:"platform"`
	// Token is the push token the platform gave the device.
	Token string `json:"token"`
	Name  string `json:"name,omitempty"`
}

// Push registers this device so that functions can reach the signed-in user: c.Push(). Web push
// subscriptions need a browser and are not part of the Go client.
type Push struct {
	c *Client

	mu       sync.Mutex
	deviceID string
}

func newPush(c *Client) *Push {
	p := &Push{c: c}
	c.auth.OnChange(func(ch AuthChange) {
		if ch.Event == SignedOut || ch.Event == SessionExpired {
			p.mu.Lock()
			p.deviceID = ""
			p.mu.Unlock()
		}
	})
	return p
}

// Push returns the signed-in user's push devices.
func (c *Client) Push() *Push { return c.push }

// DeviceID is the id of the device this client registered, or "".
func (p *Push) DeviceID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deviceID
}

// Register tells backd the token of this device. Registering again with the same token updates it.
func (p *Push) Register(ctx context.Context, in DeviceInput, opts ...RequestOption) (*Device, error) {
	var d Device
	if _, err := p.c.Do(ctx, http.MethodPost, []string{"_auth", "devices"}, in, &d, opts...); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.deviceID = d.ID
	p.mu.Unlock()
	return &d, nil
}

// Devices lists the user's registered devices.
func (p *Push) Devices(ctx context.Context, opts ...RequestOption) ([]Device, error) {
	var out struct {
		Items []Device `json:"items"`
	}
	if _, err := p.c.Do(ctx, http.MethodGet, []string{"_auth", "devices"}, nil, &out, opts...); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Unregister removes a device (this client's, with no id). It returns false when there was none to remove.
func (p *Push) Unregister(ctx context.Context, id string, opts ...RequestOption) (bool, error) {
	target := id
	if target == "" {
		target = p.DeviceID()
	}
	if target == "" {
		return false, nil
	}
	if _, err := p.c.Do(ctx, http.MethodDelete, []string{"_auth", "devices", target}, nil, nil, opts...); err != nil {
		return false, err
	}
	p.mu.Lock()
	if target == p.deviceID {
		p.deviceID = ""
	}
	p.mu.Unlock()
	return true, nil
}

// RegisterOnLaunch registers the device now (if signed in) and after every sign-in, with the
// token getToken returns; it returns nil when the platform has none yet. Errors go to onError
// (they have nobody to be returned to). The returned function stops it.
func (p *Push) RegisterOnLaunch(getToken func(ctx context.Context) (*DeviceInput, error), onError func(error)) (stop func()) {
	var mu sync.Mutex
	active := true
	run := func() {
		mu.Lock()
		on := active
		mu.Unlock()
		if !on {
			return
		}
		in, err := getToken(p.c.life)
		if err == nil && in != nil {
			_, err = p.Register(p.c.life, *in)
		}
		if err != nil && onError != nil {
			onError(err)
		}
	}
	off := p.c.auth.OnChange(func(ch AuthChange) {
		if ch.Event == SignedIn {
			go run()
		}
	})
	go func() {
		if ok, _ := p.c.auth.HasSession(p.c.life); ok {
			run()
		}
	}()
	return func() {
		mu.Lock()
		active = false
		mu.Unlock()
		off()
	}
}
