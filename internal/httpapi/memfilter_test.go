package httpapi

import "github.com/fernandezvara/backd/internal/storage"

// matches evaluates a storage filter against an in-memory document, with
// MongoDB's semantics (storage.Match).
func matches(doc map[string]any, f storage.Filter) bool {
	ok, err := storage.Match(doc, f)
	if err != nil {
		panic(err)
	}
	return ok
}
