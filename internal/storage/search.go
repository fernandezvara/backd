package storage

import (
	"slices"
	"strings"
)

// MatchSearch is an approximation of the database's text search over a document held in
// memory, for tests and fakes: words match any text containing them (case-insensitively, no
// stemming, no stop words), "quoted phrases" the text containing the phrase, and -excluded
// words reject the document. The score is the number of terms found. The database is the
// reference for real searches; this only needs the same shape of answer.
func MatchSearch(doc Document, text string) (score float64, ok bool) {
	var texts []string
	var collect func(v any)
	collect = func(v any) {
		switch t := v.(type) {
		case string:
			texts = append(texts, strings.ToLower(t))
		case []any:
			for _, e := range t {
				collect(e)
			}
		case map[string]any:
			for k, e := range t {
				if k != "_meta" && k != "id" {
					collect(e)
				}
			}
		}
	}
	for k, v := range doc {
		if k != "_meta" && k != "id" {
			collect(v)
		}
	}
	has := func(term string) bool {
		return slices.ContainsFunc(texts, func(t string) bool { return strings.Contains(t, term) })
	}
	any := false
	for _, term := range searchTokens(text) {
		switch {
		case term.exclude:
			if has(term.text) {
				return 0, false
			}
		case has(term.text):
			any = true
			score++
		}
	}
	return score, any
}

type searchToken struct {
	text    string
	exclude bool
}

// searchTokens splits a search text into words, phrases and excluded words, lowercased.
func searchTokens(s string) []searchToken {
	var out []searchToken
	var cur strings.Builder
	inQuote, exclude := false, false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, searchToken{text: strings.ToLower(cur.String()), exclude: exclude})
		}
		cur.Reset()
		exclude = false
	}
	for _, r := range s {
		switch {
		case r == '"':
			flush()
			inQuote = !inQuote
		case inQuote:
			cur.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		case r == '-' && cur.Len() == 0:
			exclude = true
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
