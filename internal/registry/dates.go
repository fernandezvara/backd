package registry

import (
	"maps"
	"strings"
	"time"
)

// DatesToStorage returns doc with the date fields converted to time.Time. doc
// itself is left alone (a request may be retried with the same body). A field
// that is absent or null stays so.
func (c *Collection) DatesToStorage(doc map[string]any) map[string]any {
	var dates []string
	for path, f := range c.Fields {
		if f.Date {
			dates = append(dates, path)
		}
	}
	if len(dates) == 0 {
		return doc
	}
	out := cloneMap(doc)
	for _, path := range dates {
		convertAt(out, strings.Split(path, "."))
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

// convertAt converts the string at path inside m (a map this function owns).
func convertAt(m map[string]any, path []string) {
	v, ok := m[path[0]]
	if !ok {
		return
	}
	if len(path) > 1 {
		if sub, ok := v.(map[string]any); ok {
			sub = cloneMap(sub) // don't touch the caller's copy
			m[path[0]] = sub
			convertAt(sub, path[1:])
		}
		return
	}
	if s, ok := v.(string); ok {
		// The schema checked it is an RFC 3339 date-time.
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			m[path[0]] = t.UTC().Truncate(time.Millisecond)
		}
	}
}
