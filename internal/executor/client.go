package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls an executor: backd's side of POST /invoke.
type Client struct {
	URL   string // the executor, e.g. http://executor:9100
	Token string // shared secret (BACKD_EXECUTOR_TOKEN)
	HTTP  *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// No overall timeout: each invocation's context carries its deadline.
	return &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second}}
}

// Invoke runs req on the executor. An error means the executor couldn't
// be reached or refused the request; how the function ended is in Result.
func (c *Client) Invoke(ctx context.Context, req InvokeRequest) (Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/invoke", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.http().Do(hreq)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return Result{}, fmt.Errorf("executor answered %s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	var r Result
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return Result{}, fmt.Errorf("executor answer: %w", err)
	}
	return r, nil
}

// Health checks the executor answers.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("executor health: %s", res.Status)
	}
	return nil
}
