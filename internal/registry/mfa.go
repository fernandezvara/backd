package registry

import "slices"

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
