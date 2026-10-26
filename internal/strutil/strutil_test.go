package strutil

import (
	"regexp"
	"strings"
	"testing"
)

func TestPascal(t *testing.T) {
	for in, want := range map[string]string{
		"shop":      "Shop",
		"my-app":    "MyApp",
		"my_app":    "MyApp",
		"checkout2": "Checkout2",
		"a1b":       "A1b",
		"Already":   "Already",
		"":          "",
	} {
		if got := Pascal(in); got != want {
			t.Errorf("Pascal(%q) = %q, want %q", in, got, want)
		}
	}
}

// legacyPascal is the copy internal/functions had, which only broke words on - and _.
// Names are validated as ^[a-z0-9]+(?:[-_][a-z0-9]+)*$, and for those both give the same.
func legacyPascal(s string) string {
	var b strings.Builder
	upper := true
	for _, r := range s {
		switch {
		case r == '-' || r == '_':
			upper = true
		case upper && r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
			upper = false
		default:
			b.WriteRune(r)
			upper = false
		}
	}
	return b.String()
}

func TestPascalMatchesLegacyForValidNames(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)
	alphabet := []string{"a", "b", "z", "0", "1", "9", "-", "_"}
	var walk func(prefix string, depth int)
	walk = func(prefix string, depth int) {
		if valid.MatchString(prefix) && Pascal(prefix) != legacyPascal(prefix) {
			t.Fatalf("%q: Pascal %q, legacy %q", prefix, Pascal(prefix), legacyPascal(prefix))
		}
		if depth == 0 {
			return
		}
		for _, c := range alphabet {
			walk(prefix+c, depth-1)
		}
	}
	walk("", 6)
}
