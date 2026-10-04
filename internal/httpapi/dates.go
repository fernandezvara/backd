package httpapi

import "time"

// Fields marked x-backd-store: date are RFC 3339 strings in the API and BSON
// Dates in the database. The conversion happens at the edges of a request:
//
//   - on the way in, after the schema accepted the strings, Collection.DatesToStorage turns
//     them into time.Time (UTC, to the millisecond MongoDB keeps), so the
//     rules, the storage and the queries all see dates;
//   - on the way out, timesToStrings turns every time.Time back into the text
//     the API speaks, the same form _meta timestamps have.
//
// Stored dates have millisecond precision and no offset: "2026-09-01T14:00:00+02:00"
// reads back as "2026-09-01T12:00:00.000Z".

// timesToStrings returns v with every time.Time turned into its RFC 3339 text
// (UTC, milliseconds), in new maps and slices: v is not modified.
func timesToStrings(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(timeFormat)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = timesToStrings(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = timesToStrings(e)
		}
		return out
	}
	return v
}
