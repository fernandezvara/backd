// Package strutil has small string helpers shared by packages that must not import each other.
// It imports only the standard library.
package strutil

import "strings"

// Pascal turns a name into PascalCase: it drops everything that is not a letter or a digit,
// capitalises the letter that follows a dropped run (and the first one), and leaves the rest
// of each word as it is. "my-app" and "my_app" give "MyApp".
func Pascal(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			if up {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			up = false
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			up = false
		default:
			up = true
		}
	}
	return b.String()
}
