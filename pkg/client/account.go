package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Account is the signed-in user's own data: ask for a copy of everything the realm holds about
// them, and download it. c.Account().
type Account struct{ c *Client }

// Account returns the signed-in user's own data.
func (c *Client) Account() *Account { return &Account{c: c} }

// DataExport is a copy of a user's data, prepared in the background.
type DataExport struct {
	ID string `json:"id"`
	// Status is "queued", "running", "ready", "failed" or "expired". A ready export can be
	// downloaded until ExpiresAt; a failed one may be asked for again at once.
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	// ExpiresAt is when the package is deleted (ready only).
	ExpiresAt string `json:"expires_at,omitempty"`
	// Size is the bytes of the zip (ready only).
	Size int64 `json:"size,omitempty"`
	// Counts is what the package holds (ready only): records per part, and "files".
	Counts map[string]int `json:"counts,omitempty"`
	// Step is what it is doing now (queued or running only).
	Step string `json:"step,omitempty"`
}

// RequestExport asks for a copy of the user's data; it needs a recent proof (Auth.Reauth).
func (a *Account) RequestExport(ctx context.Context, opts ...RequestOption) (*DataExport, error) {
	var out DataExport
	if _, err := a.c.Do(ctx, http.MethodPost, []string{"_auth", "me", "export"}, nil, &out, append(opts, WithReauth())...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Exports lists the user's exports.
func (a *Account) Exports(ctx context.Context, opts ...RequestOption) ([]DataExport, error) {
	var out struct {
		Items []DataExport `json:"items"`
	}
	if _, err := a.c.Do(ctx, http.MethodGet, []string{"_auth", "me", "exports"}, nil, &out, append([]RequestOption{WithoutCache()}, opts...)...); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Export returns one export.
func (a *Account) Export(ctx context.Context, id string, opts ...RequestOption) (*DataExport, error) {
	var out DataExport
	if _, err := a.c.Do(ctx, http.MethodGet, []string{"_auth", "me", "exports", id}, nil, &out, append([]RequestOption{WithoutCache()}, opts...)...); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExportWaitOptions are the options of WaitForExport.
type ExportWaitOptions struct {
	// PollInterval is the time between polls; 2s when zero.
	PollInterval time.Duration
	// Timeout gives up; 30 minutes when zero.
	Timeout    time.Duration
	OnProgress func(*DataExport)
}

// WaitForExport polls until the export is ready. A failed or expired export is an error.
func (a *Account) WaitForExport(ctx context.Context, id string, o ExportWaitOptions, opts ...RequestOption) (*DataExport, error) {
	interval, timeout := o.PollInterval, o.Timeout
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	end := time.Now().Add(timeout)
	for {
		e, err := a.Export(ctx, id, opts...)
		if err != nil {
			return nil, err
		}
		if o.OnProgress != nil {
			o.OnProgress(e)
		}
		switch e.Status {
		case "ready":
			return e, nil
		case "failed":
			return nil, fmt.Errorf("client: the export failed: ask for a new one")
		case "expired":
			return nil, fmt.Errorf("client: the export expired")
		}
		if time.Now().Add(interval).After(end) {
			return nil, fmt.Errorf("client: the export is still %s after %s", e.Status, timeout)
		}
		if err := sleep(ctx, interval); err != nil {
			return nil, networkError(ctx, err)
		}
	}
}

// DownloadExport copies the package of a ready export (a zip) to dst and returns how many bytes
// were written. The server answers with the bytes or with a link to the storage; both work.
func (a *Account) DownloadExport(ctx context.Context, id string, dst io.Writer, opts ...RequestOption) (int64, error) {
	res, err := a.c.Do(ctx, http.MethodGet, []string{"_auth", "me", "exports", id, "download"}, nil, nil,
		append([]RequestOption{WithRaw(), WithHeader("Accept", "application/json")}, opts...)...)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		return io.Copy(dst, res.Body)
	}
	var link struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&link); err != nil {
		return 0, newError(res.Status, "invalid_response", "the export link is not the JSON expected: "+err.Error(), kBackd)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return 0, newError(0, "invalid_request", "the export link is not usable: "+err.Error(), kValidation)
	}
	stored, err := a.c.doer.Do(req)
	if err != nil {
		return 0, networkError(ctx, err)
	}
	defer stored.Body.Close()
	if stored.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("client: the export link answered %d", stored.StatusCode)
	}
	return io.Copy(dst, stored.Body)
}
