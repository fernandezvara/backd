package push

import (
	"errors"
	"fmt"
	"slices"
)

// ErrUnknownKind is a kind the realm has no template for.
var ErrUnknownKind = errors.New("unknown push kind")

// Prepare writes the message of a send in every language the realm lists and checks each one,
// so a send that can't be delivered is refused before anything is queued. kind is a template
// kind, or "" / RawKind for a message whose title and body the function wrote; in is the
// message as the function gave it (its Data is also what templates see as .Data). The result
// has one message per locale; raw messages are the same text in all of them.
func Prepare(tpl *Templates, realm string, locales []string, targets []Target, kind string, in Message) (map[string]Message, error) {
	out := make(map[string]Message, len(locales))
	templated := kind != "" && kind != RawKind
	if templated {
		if !tpl.HasKind(kind) {
			return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
		}
		if in.Title != "" || in.Body != "" {
			return nil, &ValidationError{Field: "title", Reason: "a message with a kind takes its text from the template: give kind, or title and body"}
		}
	}
	data := make(map[string]any, len(in.Data))
	for k, v := range in.Data {
		data[k] = v
	}
	for _, locale := range locales {
		m := in
		if templated {
			r, err := tpl.Render(kind, locale, Data{Realm: realm, Data: data})
			if err != nil {
				return nil, err
			}
			m.Kind, m.Title, m.Body = kind, r.Title, r.Body
			if m.Link == "" {
				m.Link = r.Link
			}
		} else {
			m.Kind = RawKind
		}
		if err := m.Validate(); err != nil {
			if templated {
				return nil, fmt.Errorf("the %s text of %q: %w", locale, kind, err)
			}
			return nil, err
		}
		if err := CheckSize(targets, m); err != nil {
			if _, ok := errors.AsType[*TooLargeError](err); ok {
				return nil, fmt.Errorf("in %s: %w", locale, err)
			}
			return nil, err
		}
		out[locale] = m
	}
	return out, nil
}

// TargetsOf lists the targets of the enabled platforms, in a stable order.
func TargetsOf(platforms map[string]string) []Target {
	names := make([]string, 0, len(platforms))
	for p := range platforms {
		names = append(names, p)
	}
	slices.Sort(names)
	out := make([]Target, 0, len(names))
	for _, p := range names {
		out = append(out, Target{Platform: p, Provider: platforms[p]})
	}
	return out
}
