package httpapi

import (
	"errors"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var printer = message.NewPrinter(language.English)

// validationDetails turns a schema validation error into one Detail per
// failing path, sorted by path.
func validationDetails(err error) []Detail {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return []Detail{{Path: "", Reason: err.Error()}}
	}
	var out []Detail
	collectDetails(verr, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func collectDetails(e *jsonschema.ValidationError, out *[]Detail) {
	path := strings.Join(e.InstanceLocation, ".")
	switch k := e.ErrorKind.(type) {
	case *kind.Schema, *kind.Group, *kind.AllOf, *kind.Reference:
		for _, c := range e.Causes {
			collectDetails(c, out)
		}
	case *kind.Required:
		for _, m := range k.Missing {
			*out = append(*out, Detail{Path: joinPath(path, m), Reason: "is required"})
		}
	case *kind.AdditionalProperties:
		for _, p := range k.Properties {
			*out = append(*out, Detail{Path: joinPath(path, p), Reason: "is not allowed"})
		}
	default:
		*out = append(*out, Detail{Path: path, Reason: e.ErrorKind.LocalizedString(printer)})
	}
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
