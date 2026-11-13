package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/fernandezvara/backd/internal/auth/passkey"
)

// A passkey as the first method of a strategy (design/passkeys.md §6): the options carry no email
// and answer alike for everyone; the verification applies the same account checks as the password
// sign-in and continues in the strategy engine.

// ErrInvalidPasskey is every way a passkey sign-in can fail, whatever the cause: unknown credential,
// bad signature, wrong origin, expired challenge, a banned or unverified user, a cloned key. The
// reason goes to the audit log, never to the caller.
var ErrInvalidPasskey = errors.New("the passkey did not work")

// Audit events of passkey sign-ins. passkey.login_failed carries the reason.
const (
	AuditPasskeyLogin          = "passkey.login"
	AuditPasskeyLoginFailed    = "passkey.login_failed"
	AuditPasskeyCloneSuspected = "passkey.clone_suspected"
)

// passkeyOptionsRate is how many sign-in ceremonies one address may begin a minute. Each stores a
// challenge for five minutes, so this is also what bounds the storage a flood can take.
const passkeyOptionsRate = 60

// PasskeyOptions are what the app passes to navigator.credentials.get.
type PasskeyOptions struct {
	Options  []byte
	Ceremony string
	Strategy string
}

// BeginPasskeySignIn starts a sign-in with a passkey as the first method of a strategy. org is the
// organization route it was asked on ("" for the realm's own door). It needs no user and answers the
// same whether or not anyone has a passkey.
func (s *Users) BeginPasskeySignIn(ctx context.Context, strategy, org, ip string) (PasskeyOptions, error) {
	if !s.Settings.Passkeys.Enabled {
		return PasskeyOptions{}, ErrPasskeysDisabled
	}
	st, err := s.Settings.StrategyFor(strategy, MethodPasskey)
	if err != nil {
		return PasskeyOptions{}, err
	}
	if ip != "" {
		if err := s.RateLimit(ctx, "passkey-options:"+ip, passkeyOptionsRate, time.Minute); err != nil {
			return PasskeyOptions{}, err
		}
	}
	meta := &ceremonyMeta{strategy: st.Name, org: org}
	svc, err := s.passkeyService(meta)
	if err != nil {
		return PasskeyOptions{}, err
	}
	options, ceremony, err := svc.BeginLogin(ctx, "")
	if err != nil {
		return PasskeyOptions{}, err
	}
	return PasskeyOptions{Options: options, Ceremony: ceremony, Strategy: st.Name}, nil
}

// PasskeySignIn is the second call of a sign-in with a passkey.
type PasskeySignIn struct {
	Ceremony   string
	Credential []byte // the JSON of the PublicKeyCredential
	IP         string
	// Org is the organization route the call came on ("" for the realm's own door); it must be the
	// one the options were asked on.
	Org string
}

// SignInWithPasskey verifies the assertion and goes on in the strategy engine: a session, the
// consent challenge, or the challenge for the strategy's next step. Every failure is
// ErrInvalidPasskey (or a *ThrottledError / *MFALockedError for a limit).
func (s *Users) SignInWithPasskey(ctx context.Context, in PasskeySignIn) (Redeemed, error) {
	ks := keys(s.Settings.Throttle(), "", in.IP)
	if err := s.checkThrottle(ctx, ks); err != nil {
		return Redeemed{}, err
	}
	// A credential we know names its user: that user's failures are counted together, and a user
	// locked out is not tried.
	known := s.passkeyUserOf(ctx, in.Credential)
	if known != "" {
		release, err := s.lockLogin(ctx, mfaUserKey(known))
		if err != nil {
			return Redeemed{}, err
		}
		defer release()
		if err := s.checkMFALock(ctx, known); err != nil {
			return Redeemed{}, err
		}
	}
	fail := func(reason string, details map[string]any) (Redeemed, error) {
		if details == nil {
			details = map[string]any{}
		}
		details["reason"] = reason
		target := "anonymous"
		if known != "" {
			target = userTarget(known)
		}
		s.Audit(ctx, AuditPasskeyLoginFailed, target, details)
		s.Metrics.PasskeyEvent(s.Realm, "login_failed")
		errs := []error{s.recordFailure(ctx, ks)}
		if known != "" {
			now := s.now()
			errs = append(errs, s.Store.RecordLoginFailure(ctx, mfaUserKey(known), now, now.Add(mfaFailureWindow)))
		}
		if err := errors.Join(errs...); err != nil {
			return Redeemed{}, err
		}
		return Redeemed{}, ErrInvalidPasskey
	}

	meta := &ceremonyMeta{}
	svc, err := s.passkeyService(meta)
	if err != nil {
		return fail("passkeys_disabled", nil)
	}
	res, err := svc.FinishLogin(ctx, in.Ceremony, in.Credential)
	if err != nil {
		var pe *passkey.Error
		var reason string
		switch {
		case errors.As(err, &pe):
			reason = pe.Reason
		case errors.Is(err, passkey.ErrUnknownCredential):
			reason = "unknown_credential"
		case errors.Is(err, passkey.ErrCeremony):
			reason = "unknown_or_expired_challenge"
		default:
			return Redeemed{}, err // the store failed: not a wrong passkey
		}
		return fail(reason, nil)
	}
	if meta.org != in.Org {
		return fail("another_route", nil)
	}
	u, err := s.Store.UserByID(ctx, res.UserID)
	if err != nil {
		return fail("unknown_user", nil)
	}
	known = u.ID
	switch {
	case u.Disabled:
		return fail("disabled", nil)
	case !u.ErasedAt.IsZero():
		return fail("erased", nil)
	case s.Settings.Account.RequireVerifiedEmail && !u.EmailVerified:
		return fail("email_not_verified", nil)
	case !u.LoginNetworks.Allows(in.IP):
		return fail("network", nil)
	}
	h := handover{strategy: meta.strategy, passkey: res.Credential.ID, passkeySynced: res.BackupEligible}
	switch {
	case in.Org != "":
		org, err := s.OrgByRef(ctx, in.Org)
		if err != nil {
			return fail("organization", nil)
		}
		if _, err := s.Store.OrgMember(ctx, org.ID, u.ID); err != nil {
			return fail("not_a_member", nil)
		}
		h.org = org.ID
	case s.Settings.Organizations != nil && !s.Settings.IsAdmin(u.Roles):
		// The realm's own door is the operators' alone (design/scoped-sessions.md §5.5).
		return fail("not_an_operator", nil)
	}
	if res.CounterRegressed {
		s.Audit(ctx, AuditPasskeyCloneSuspected, userTarget(u.ID), map[string]any{"passkey": res.Credential.ID, "name": res.Credential.Name})
		s.Metrics.PasskeyEvent(s.Realm, "clone_suspected")
		s.securityNotice(ctx, u, "passkey-clone-suspected", 0)
		return fail("counter_regressed", map[string]any{"passkey": res.Credential.ID})
	}
	_ = s.Store.ClearLoginAttempts(ctx, mfaUserKey(u.ID))
	s.Audit(ctx, AuditPasskeyLogin, userTarget(u.ID), map[string]any{"passkey": res.Credential.ID, "synced": res.BackupEligible, "strategy": meta.strategy})
	s.Metrics.PasskeyEvent(s.Realm, "login")
	return s.afterFirstFactor(ctx, u, MethodPasskey, h)
}

// passkeyUserOf names the user of the credential in a sign-in body, "" when it is not a credential we
// hold (or the body is not one).
func (s *Users) passkeyUserOf(ctx context.Context, body []byte) string {
	var b struct {
		RawID string `json:"rawId"`
		ID    string `json:"id"`
	}
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	id := b.RawID
	if id == "" {
		id = b.ID
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) == 0 {
		return ""
	}
	c, err := s.Store.ByCredentialID(ctx, raw)
	if err != nil {
		return ""
	}
	return c.UserID
}
