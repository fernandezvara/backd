package registry

import (
	"errors"
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

// MFASettings are the realm's multi-factor settings (mfa in realm.yaml).
type MFASettings struct {
	// Off says users may not turn TOTP on (mfa.enabled: false). Users that already have
	// it are still asked for it: switching the feature off never weakens an account.
	Off bool
	// Issuer is the name authenticator apps show; the realm's name when empty.
	Issuer string
	// RequiredRoles are the roles whose holders must use MFA; RequiredAll means everyone.
	RequiredRoles []string
	RequiredAll   bool
}

// Required says whether a user with these roles must use MFA.
func (m MFASettings) Required(roles []string) bool {
	if m.RequiredAll {
		return true
	}
	for _, r := range roles {
		if slices.Contains(m.RequiredRoles, r) {
			return true
		}
	}
	return false
}

// parseRequiredRoles reads mfa.required_roles: "all", or a list of declared role names.
func (s *RealmSettings) parseRequiredRoles(n yaml.Node) []error {
	if n.Kind == 0 {
		return nil
	}
	switch {
	case n.Kind == yaml.ScalarNode && n.Value == "all":
		s.MFA.RequiredAll = true
		return nil
	case n.Kind == yaml.ScalarNode:
		return []error{fmt.Errorf("mfa.required_roles: %q isn't a list of roles or \"all\"", n.Value)}
	case n.Kind != yaml.SequenceNode:
		return []error{errors.New("mfa.required_roles: must be a list of roles, or \"all\"")}
	}
	var errs []error
	for i, item := range n.Content {
		role := item.Value
		switch {
		case item.Kind != yaml.ScalarNode || role == "":
			errs = append(errs, fmt.Errorf("mfa.required_roles[%d]: must be a role name", i))
		case role == "all":
			errs = append(errs, fmt.Errorf("mfa.required_roles[%d]: \"all\" is a value of required_roles, not a role in a list", i))
		case !slices.Contains(s.MFA.RequiredRoles, role):
			if _, ok := s.Roles[role]; !ok {
				errs = append(errs, fmt.Errorf("mfa.required_roles[%d]: role %q isn't declared in realm.yaml", i, role))
				continue
			}
			s.MFA.RequiredRoles = append(s.MFA.RequiredRoles, role)
		default:
			errs = append(errs, fmt.Errorf("mfa.required_roles[%d]: %q is listed twice", i, role))
		}
	}
	return errs
}
