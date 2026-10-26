package exportpkg

import (
	"strings"

	"github.com/fernandezvara/backd/internal/docutil"
)

// Select keeps of a document its id and the given field paths ("a", "a.b", "_meta.owner"):
// a path naming an object keeps all of it, one that is missing in the document adds nothing.
// "_meta" is only present when a path asks for it.
func Select(doc map[string]any, paths []string) map[string]any {
	out := map[string]any{}
	if id, ok := doc["id"]; ok {
		out["id"] = id
	}
	for _, p := range paths {
		docutil.CopyPath(out, doc, strings.Split(p, "."))
	}
	return out
}
