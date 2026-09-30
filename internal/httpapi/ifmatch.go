package httpapi

import (
	"fmt"
	"strconv"
	"strings"
)

// ifMatch is a parsed If-Match precondition.
type ifMatch struct {
	present  bool
	any      bool    // "*": any existing document
	versions []int64 // strong entity tags "<version>"
}

// specific reports whether the precondition names concrete versions.
func (c ifMatch) specific() bool { return c.present && !c.any }

// matches reports whether a document at version v satisfies the precondition.
func (c ifMatch) matches(v int64) bool {
	if !c.present || c.any {
		return true
	}
	for _, want := range c.versions {
		if want == v {
			return true
		}
	}
	return false
}

// parseIfMatch parses If-Match header values: "*" or a comma-separated list
// of entity tags. Weak tags (W/"…") never match, as If-Match requires
// strong comparison. Tags that aren't backd versions are rejected.
func parseIfMatch(values []string) (ifMatch, error) {
	var c ifMatch
	for _, v := range values {
		for _, tag := range strings.Split(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			c.present = true
			switch {
			case tag == "*":
				c.any = true
			case strings.HasPrefix(tag, "W/"):
				// weak: never matches
			default:
				n, err := strconv.ParseInt(strings.Trim(tag, `"`), 10, 64)
				if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' || err != nil || n < 0 {
					return ifMatch{}, fmt.Errorf(`If-Match must be "*" or quoted document versions such as "3", got %s`, tag)
				}
				c.versions = append(c.versions, n)
			}
		}
	}
	return c, nil
}
