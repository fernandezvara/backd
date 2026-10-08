package rulestest

import "strings"

// rulesSection is a collection.yaml holding the given rules: what a rules.yaml held before
// the rules became a section of collection.yaml. Append it to the rest of the file when the
// test has one.
func rulesSection(rules string) string {
	var b strings.Builder
	b.WriteString("rules:\n")
	for _, line := range strings.Split(strings.TrimRight(rules, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}
