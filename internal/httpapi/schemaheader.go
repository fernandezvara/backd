package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// schemaVersionHeader is how an app says which version of each collection's schema it was built
// for (design/schema-versioning.md §9.2): `<database>/<collection>=<n>[, …]`.
const schemaVersionHeader = "X-Backd-Schema-Version"

const (
	codeInvalidSchemaVersion = "invalid_schema_version"
	codeSchemaVersionUnknown = "schema_version_unknown"
	codeUpgradeRequired      = "upgrade_required"
)

// parseSchemaVersions reads the header's values: a list of <database>/<collection>=<n>, n a
// positive number. An empty or malformed entry is an error naming it.
func parseSchemaVersions(values []string) (map[string]int, error) {
	var out map[string]int
	for _, v := range values {
		for _, entry := range strings.Split(v, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				return nil, fmt.Errorf("an empty entry: want <database>/<collection>=<n>")
			}
			key, num, ok := strings.Cut(entry, "=")
			key, num = strings.TrimSpace(key), strings.TrimSpace(num)
			database, collection, slash := strings.Cut(key, "/")
			if !ok || !slash || !registry.ValidName(database) || !registry.ValidName(collection) {
				return nil, fmt.Errorf("%q: want <database>/<collection>=<n>", entry)
			}
			n, err := strconv.Atoi(num)
			if err != nil || n < 1 || (len(num) > 1 && num[0] == '0') {
				return nil, fmt.Errorf("%q: the version must be a positive whole number", entry)
			}
			if out == nil {
				out = map[string]int{}
			}
			if prev, dup := out[key]; dup && prev != n {
				return nil, fmt.Errorf("%q: declared twice with different versions", key)
			}
			out[key] = n
		}
	}
	return out, nil
}

// declaredSchemaVersions is what the request declares, or an error for a malformed header.
func declaredSchemaVersions(r *http.Request) (map[string]int, error) {
	values := r.Header.Values(schemaVersionHeader)
	if len(values) == 0 {
		return nil, nil
	}
	return parseSchemaVersions(values)
}

// declaredFor returns the version the request declares for a versioned collection. declared is
// false when it says nothing about it, and for a collection without versioned schema files (its
// entry is ignored).
func declaredFor(r *http.Request, c *registry.Collection) (n int, declared bool, err error) {
	if !c.Versioned {
		_, err = declaredSchemaVersions(r) // a malformed header is refused whatever it names
		return 0, false, err
	}
	all, err := declaredSchemaVersions(r)
	if err != nil {
		return 0, false, err
	}
	n, declared = all[c.Database+"/"+c.Name]
	return n, declared, nil
}

// schemaGate refuses a request whose declared schema version the collection can't serve: a
// malformed header (400 invalid_schema_version), a version above the current one (400
// schema_version_unknown: deploy the server before the apps) or below min_schema_version (426
// upgrade_required). It answers itself and returns false then. A request that declares nothing
// is never refused.
func schemaGate(w http.ResponseWriter, r *http.Request, c *registry.Collection) bool {
	n, declared, err := declaredFor(r, c)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidSchemaVersion, "the "+schemaVersionHeader+" header is malformed: "+err.Error())
		return false
	}
	if !declared {
		return true
	}
	key := c.Database + "/" + c.Name
	switch {
	case n > c.SchemaVersion:
		writeError(w, r, http.StatusBadRequest, codeSchemaVersionUnknown,
			fmt.Sprintf("the app declares version %d of %s, but the server has version %d: deploy the server before the apps", n, key, c.SchemaVersion))
		return false
	case c.MinSchemaVersion > 0 && n < c.MinSchemaVersion:
		writeJSON(w, http.StatusUpgradeRequired, errorBody{Error: errorPayload{
			Code:       codeUpgradeRequired,
			Message:    fmt.Sprintf("this app was built for version %d of %s and the server needs at least version %d: update the app", n, key, c.MinSchemaVersion),
			Collection: key, Min: c.MinSchemaVersion, Declared: n,
			RequestID: requestID(r.Context()),
		}})
		return false
	}
	return true
}

// writtenSchemaVersion is the version a write to c is stamped with: what the request declares for
// it, else the collection's current version. (schemaGate has already refused a declared version
// above the current one.)
func writtenSchemaVersion(r *http.Request, c *registry.Collection) int {
	if n, declared, err := declaredFor(r, c); err == nil && declared && n <= c.SchemaVersion {
		return n
	}
	return c.SchemaVersion
}
