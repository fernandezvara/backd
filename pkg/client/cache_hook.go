package client

import (
	"net/http"
)

// cacheCtx is the revalidation cache as one call sees it. Until the cache exists (see cache.go)
// every method does nothing.
type cacheCtx struct{}

type cacheEntry struct{}

func (c *Client) cacheFor(*request, http.Header, string) *cacheCtx { return nil }

func (*cacheCtx) lookup(string, http.Header) *cacheEntry { return nil }

func (*cacheCtx) answer(*http.Response, string, *cacheEntry, *request) (bool, *Response, error) {
	return false, nil, nil
}

func (*cacheCtx) store(string, *http.Response, []byte) {}
