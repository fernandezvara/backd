// Package pushsend holds backd's built-in push senders: FCM (HTTP v1), APNs (HTTP/2, token
// authentication) and Web Push (VAPID, RFC 8291). Each answers one result per device for a
// delivery unit; the worker's job around them (limits, retries, pruning) is elsewhere.
package pushsend

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fernandezvara/backd/internal/egress"
	"github.com/fernandezvara/backd/internal/push"
)

// Config says how the senders reach the providers.
type Config struct {
	// Proxy is the egress proxy URL (EXECUTOR_PROXY) and EgressKey the key that signs the
	// token each unit presents to it. Without a proxy the senders dial the providers directly,
	// and only public addresses.
	Proxy     string
	EgressKey []byte
	Now       func() time.Time

	// Test hooks: the providers' addresses, and the roots that vouch for them.
	FCMBase   string // https://fcm.googleapis.com
	OAuthURL  string // the service account's token_uri
	APNsBase  string // https://api.push.apple.com
	APNsSand  string // https://api.sandbox.push.apple.com
	TLS       *tls.Config
	AllowAddr bool // dial private addresses directly (tests)
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Senders returns the built-in senders by provider name.
func Senders(cfg Config) map[string]push.Sender {
	fcm := &fcmSender{cfg: &cfg, tokens: map[string]cachedToken{}}
	return map[string]push.Sender{
		push.ProviderFCM:     fcm,
		push.ProviderAPNs:    &apnsSender{cfg: &cfg, jwts: map[string]cachedToken{}},
		push.ProviderWebPush: &webSender{cfg: &cfg},
	}
}

type cachedToken struct {
	value   string
	expires time.Time
}

// errCredentials is a failure of the call as a whole: the provider refused the realm's
// credentials, so every device of the unit is tried again later.
var errCredentials = errors.New("the provider refused the realm's credentials")

// httpFor makes the HTTP client of one delivery unit, which may reach only the hosts it
// names, through the egress proxy when there is one. Connections are reused within the unit.
func (c *Config) httpFor(hosts []string, timeout time.Duration) (*http.Client, func(), error) {
	tr := &http.Transport{
		TLSClientConfig:       c.TLS,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       30 * time.Second,
	}
	if c.Proxy != "" {
		u, err := url.Parse(c.Proxy)
		if err != nil || u.Host == "" {
			return nil, nil, fmt.Errorf("invalid egress proxy URL %q", c.Proxy)
		}
		token := egress.Sign(c.EgressKey, egress.Claims{Hosts: hosts, Expires: c.now().Add(timeout + 30*time.Second)})
		u.User = url.UserPassword(token, "")
		tr.Proxy = http.ProxyURL(u)
	} else {
		allowed := map[string]bool{}
		for _, h := range hosts {
			allowed[strings.ToLower(h)] = true
		}
		dialer := &net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				if c.AllowAddr {
					return nil
				}
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip, err := netip.ParseAddr(host)
				if err != nil || !publicAddr(ip) {
					return fmt.Errorf("%s is not a public address", host)
				}
				return nil
			},
		}
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil || (!allowed[strings.ToLower(host)] && !allowed[strings.ToLower(addr)]) {
				return nil, fmt.Errorf("the host %s is not one of the provider's", addr)
			}
			_ = port
			return dialer.DialContext(ctx, network, addr)
		}
	}
	client := &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client, tr.CloseIdleConnections, nil
}

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

// eachDelivery runs fn for every delivery, parallelism at a time, and collects the results in
// order. A fn error that is errCredentials stops the unit: the first one is returned.
func eachDelivery(ctx context.Context, deliveries []push.Delivery, parallelism int, fn func(ctx context.Context, d push.Delivery) (push.DeviceResult, error)) ([]push.DeviceResult, error) {
	if parallelism < 1 {
		parallelism = 1
	}
	results := make([]push.DeviceResult, len(deliveries))
	sem := make(chan struct{}, parallelism)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for i, d := range deliveries {
		mu.Lock()
		stop := first != nil
		mu.Unlock()
		if stop || ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			r, err := fn(ctx, d)
			if err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
				return
			}
			results[i] = r
		}()
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// statusOf maps an HTTP status the providers share: ok, throttled or transient, and what is
// left as a permanent failure.
func transient(status int) bool { return status == http.StatusTooManyRequests || status >= 500 }

// scrub makes a transport error safe to log: the URL of a request holds a device's token (APNs)
// or the secret path of a push subscription (Web Push), so only the host and the cause stay.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		host := ue.URL
		if u, perr := url.Parse(ue.URL); perr == nil {
			host = u.Host
		}
		return fmt.Errorf("%s %s: %w", ue.Op, host, ue.Err)
	}
	return err
}
