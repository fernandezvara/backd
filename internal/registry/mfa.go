package registry

// MFASettings are the realm's TOTP settings (auth.totp in realm.yaml).
type MFASettings struct {
	// Off says users may not turn TOTP on (auth.totp.enabled: false). Users that already have
	// it still can use it where a strategy asks for it: switching the feature off never
	// weakens an account.
	Off bool
	// Issuer is the name authenticator apps show; the realm's name when empty.
	Issuer string
}
