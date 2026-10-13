package registry

import (
	"fmt"
	"slices"
)

// FunctionPush is what a function's `push:` declares: that it may send pushes, which template
// kinds it sends (empty: any, and raw title and body), and whether it may address an audience
// rather than named users.
type FunctionPush struct {
	// Kinds are the template kinds the function declares; Restricted says `kinds:` was given.
	Kinds      []string
	Restricted bool
	Audience   bool
}

// AllowsKind says whether the function may send the kind.
func (f *FunctionPush) AllowsKind(kind string) bool {
	return f != nil && (!f.Restricted || slices.Contains(f.Kinds, kind))
}

// parseFunctionPush reads `push: true` and `push: { kinds: [...], audience: true }`.
func parseFunctionPush(v any) (*FunctionPush, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case bool:
		if !x {
			return nil, nil
		}
		return &FunctionPush{}, nil
	case map[string]any:
		out := &FunctionPush{}
		for k, val := range x {
			switch k {
			case "kinds":
				list, ok := val.([]any)
				if !ok {
					return nil, fmt.Errorf("push.kinds: must be a list of template kinds")
				}
				out.Restricted = true
				for i, e := range list {
					s, ok := e.(string)
					if !ok || !namePattern.MatchString(s) {
						return nil, fmt.Errorf("push.kinds[%d]: %v isn't a valid kind name", i, e)
					}
					if slices.Contains(out.Kinds, s) {
						return nil, fmt.Errorf("push.kinds[%d]: %q is listed twice", i, s)
					}
					out.Kinds = append(out.Kinds, s)
				}
			case "audience":
				b, ok := val.(bool)
				if !ok {
					return nil, fmt.Errorf("push.audience: must be true or false")
				}
				out.Audience = b
			default:
				return nil, fmt.Errorf("push.%s: unknown key (use kinds and audience)", k)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("push: must be true, or a mapping with kinds and audience")
}
