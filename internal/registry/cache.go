package registry

import (
	"fmt"

	"github.com/fernandezvara/backd/internal/rules"
)

// MaxCacheAge is the longest `cache.max_age` a collection may declare, in seconds.
const MaxCacheAge = 3600

// CachePolicy is collection.yaml's cache: the collection's anonymous reads may
// be stored by shared caches (CDNs) for MaxAge seconds. Everything else stays
// private, no-cache.
type CachePolicy struct {
	Public bool
	MaxAge int // seconds
}

// cacheDoc is cache in collection.yaml.
type cacheDoc struct {
	Public bool `yaml:"public"`
	MaxAge int  `yaml:"max_age"`
}

// policy checks the declaration against the collection: public caching only
// makes sense, and is only safe, for content anyone can read.
func (d *cacheDoc) policy(c *Collection, path string) (*CachePolicy, error) {
	if d == nil || !d.Public {
		if d != nil && d.MaxAge != 0 {
			return nil, fmt.Errorf("%s: cache.max_age only applies with `public: true`", path)
		}
		return nil, nil
	}
	if d.MaxAge < 1 || d.MaxAge > MaxCacheAge {
		return nil, fmt.Errorf("%s: cache.max_age must be between 1 and %d seconds (got %d)", path, MaxCacheAge, d.MaxAge)
	}
	read := c.Rules.For(rules.Read)
	switch {
	case read == nil:
		return nil, fmt.Errorf("%s: cache.public needs a read rule that lets anonymous callers read: the collection has none", path)
	case !read.CanAllowAnonymous():
		return nil, fmt.Errorf("%s: cache.public needs a read rule that lets anonymous callers read, but `%s` never does", path, read.Source)
	case read.UsesNow():
		return nil, fmt.Errorf("%s: cache.public can't be used with a read rule that depends on `now`: a cached response would outlive what it says", path)
	}
	return &CachePolicy{Public: true, MaxAge: d.MaxAge}, nil
}
