package auth

import (
	"context"
	"errors"
	"time"

	"github.com/fernandezvara/backd/internal/auth/passkey"
	"github.com/fernandezvara/backd/internal/registry"
)

// A passkey as a later step of a strategy, as the proof of a re-authentication, and as the step a
// person with none is led to add (design/passkeys.md §7.1, §7.2, §7.4). The ceremonies here belong to
// something that already exists (a challenge, a session), so their id is derived from it and the app
// sends no ceremony id of its own.

// ErrCredentialRequired: removing the passkey would leave a holder of an admin role with no way to
// complete any strategy that role allows.
var ErrCredentialRequired = errors.New("this is the only credential that lets an administrator role sign in: add another way first")

// fixedCeremonies keeps one ceremony under an id derived from the challenge or session it belongs to;
// beginning again replaces it.
type fixedCeremonies struct {
	s  *Users
	id string
}

func (f fixedCeremonies) Put(ctx context.Context, kind string, data []byte, userID string, ttl time.Duration) (string, error) {
	now := f.s.now()
	_ = f.s.Store.DeleteChallenge(ctx, HashToken(f.id))
	err := f.s.Store.PutChallenge(ctx, AuthChallenge{ID: HashToken(f.id), Kind: kind, UserID: userID, Secret: string(data), CreatedAt: now, ExpiresAt: now.Add(ttl)})
	return f.id, err
}

func (f fixedCeremonies) Claim(ctx context.Context, id, kind string) ([]byte, string, error) {
	ch, err := f.s.Store.ClaimChallenge(ctx, HashToken(f.id), f.s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && ch.Kind != kind) {
		return nil, "", passkey.ErrCeremony
	}
	if err != nil {
		return nil, "", err
	}
	return []byte(ch.Secret), ch.UserID, nil
}

// stepService is the passkey service whose ceremony lives under the id of a challenge or a session.
func (s *Users) stepService(owner, id string) (*passkey.Service, string, error) {
	cid := owner + ":" + id
	svc, err := s.passkeyServiceWith(fixedCeremonies{s, cid})
	return svc, cid, err
}

// mfaChallengeFor resolves the token of a challenge that waits for a passkey.
func (s *Users) challengeWaitingFor(ctx context.Context, token, kind, method string) (AuthChallenge, User, error) {
	if token == "" {
		return AuthChallenge{}, User{}, ErrInvalidChallenge
	}
	c, err := s.Store.GetChallenge(ctx, HashToken(token), s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && (c.Kind != kind || s.pendingMethod(c) != method)) {
		return AuthChallenge{}, User{}, ErrInvalidChallenge
	}
	if err != nil {
		return AuthChallenge{}, User{}, err
	}
	u, err := s.Store.UserByID(ctx, c.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && (u.Disabled || !u.ErasedAt.IsZero())) {
		return AuthChallenge{}, User{}, ErrInvalidChallenge
	}
	return c, u, err
}

// BeginPasskeyStep gives the options for the passkey a sign-in challenge waits for (a later step of
// the strategy): only this user's passkeys are allowed, since the first step already said who they are.
func (s *Users) BeginPasskeyStep(ctx context.Context, challenge string) ([]byte, error) {
	c, _, err := s.challengeWaitingFor(ctx, challenge, ChallengeMFA, MethodPasskey)
	if err != nil {
		return nil, err
	}
	svc, _, err := s.stepService("mfa", c.ID)
	if err != nil {
		return nil, err
	}
	options, _, err := svc.BeginLogin(ctx, c.UserID)
	if errors.Is(err, passkey.ErrNoCredentials) {
		return nil, ErrInvalidChallenge
	}
	return options, err
}

// checkPasskeyStep verifies the answer to BeginPasskeyStep. A wrong ceremony is not an error but ok
// false, so the caller counts it like a wrong code.
func (s *Users) checkPasskeyStep(ctx context.Context, c AuthChallenge, credential []byte) (passkey.LoginResult, bool, error) {
	svc, id, err := s.stepService("mfa", c.ID)
	if err != nil {
		return passkey.LoginResult{}, false, err
	}
	return s.verifyKnownPasskey(ctx, svc, id, c.UserID, credential)
}

// verifyKnownPasskey runs the second call of a ceremony for a user already known. It answers ok false
// for anything that does not verify, for another user's credential and for a counter that went
// backwards (which is audited and told to the user); only a failing store is an error.
func (s *Users) verifyKnownPasskey(ctx context.Context, svc *passkey.Service, ceremony, userID string, credential []byte) (passkey.LoginResult, bool, error) {
	res, err := svc.FinishLogin(ctx, ceremony, credential)
	switch {
	case err == nil:
	case errors.Is(err, passkey.ErrInvalid), errors.Is(err, passkey.ErrUnknownCredential), errors.Is(err, passkey.ErrCeremony):
		s.Audit(ctx, AuditPasskeyLoginFailed, userTarget(userID), map[string]any{"reason": failureReason(err), "step": true})
		return passkey.LoginResult{}, false, nil
	default:
		return passkey.LoginResult{}, false, err
	}
	if res.UserID != userID {
		s.Audit(ctx, AuditPasskeyLoginFailed, userTarget(userID), map[string]any{"reason": "another_user", "step": true})
		return passkey.LoginResult{}, false, nil
	}
	if res.CounterRegressed {
		if u, err := s.Store.UserByID(ctx, userID); err == nil {
			s.Audit(ctx, AuditPasskeyCloneSuspected, userTarget(userID), map[string]any{"passkey": res.Credential.ID, "name": res.Credential.Name})
			s.Metrics.PasskeyEvent(s.Realm, "clone_suspected")
			s.securityNotice(ctx, u, "passkey-clone-suspected", 0)
		}
		s.Audit(ctx, AuditPasskeyLoginFailed, userTarget(userID), map[string]any{"reason": "counter_regressed", "step": true})
		return passkey.LoginResult{}, false, nil
	}
	return res, true, nil
}

// failureReason is the reason a ceremony failed, for the audit log.
func failureReason(err error) string {
	var pe *passkey.Error
	switch {
	case errors.As(err, &pe):
		return pe.Reason
	case errors.Is(err, passkey.ErrUnknownCredential):
		return "unknown_credential"
	}
	return "unknown_or_expired_challenge"
}

// BeginPasskeyEnrollment gives the options for adding the passkey a sign-in is waiting for, to a person
// who has none (the enroll challenge of a strategy with a passkey step).
func (s *Users) BeginPasskeyEnrollment(ctx context.Context, challenge string) ([]byte, error) {
	c, u, err := s.challengeWaitingFor(ctx, challenge, ChallengeEnroll, MethodPasskey)
	if err != nil {
		return nil, err
	}
	if n, err := s.Store.CountByUser(ctx, u.ID); err != nil {
		return nil, err
	} else if n >= MaxPasskeys {
		return nil, ErrPasskeyLimit
	}
	svc, _, err := s.stepService("enroll", c.ID)
	if err != nil {
		return nil, err
	}
	options, _, err := svc.BeginRegistration(ctx, passkey.UserInfo{ID: u.ID, Name: u.Email, DisplayName: u.Email})
	return options, err
}

// CompletePasskeyEnrollment stores the passkey and ends the sign-in that waited for it: the answer is
// the session (or the next step), and a passkey step makes no recovery codes. Five wrong answers end
// the challenge.
func (s *Users) CompletePasskeyEnrollment(ctx context.Context, challenge string, credential []byte, name string) (Verified, error) {
	c, u, err := s.challengeWaitingFor(ctx, challenge, ChallengeEnroll, MethodPasskey)
	if err != nil {
		return Verified{}, err
	}
	name, err = cleanPasskeyName(name)
	if err != nil {
		return Verified{}, err
	}
	svc, id, err := s.stepService("enroll", c.ID)
	if err != nil {
		return Verified{}, err
	}
	cred, err := svc.FinishRegistration(ctx, id, u.ID, credential, name)
	if errors.Is(err, passkey.ErrInvalid) || errors.Is(err, passkey.ErrCeremony) {
		cur, cerr := s.Store.CountChallengeAttempt(ctx, c.ID, s.now())
		if errors.Is(cerr, ErrNotFound) || (cerr == nil && cur.Attempts >= MaxChallengeAttempts) {
			_ = s.Store.DeleteChallenge(ctx, c.ID)
			return Verified{}, ErrInvalidChallenge
		}
		return Verified{}, err
	}
	if err != nil {
		return Verified{}, err
	}
	// One use: the sign-in ends here, whoever got here first.
	if _, err := s.Store.ClaimChallenge(ctx, c.ID, s.now()); errors.Is(err, ErrNotFound) {
		return Verified{}, ErrInvalidChallenge
	} else if err != nil {
		return Verified{}, err
	}
	s.Audit(ctx, AuditPasskeyRegistered, userTarget(u.ID), map[string]any{"passkey": cred.ID, "name": cred.Name, "synced": cred.BackupEligible, "enrolled_at_sign_in": true})
	s.Metrics.PasskeyEvent(s.Realm, "registered")
	s.securityNotice(ctx, u, "passkey-added", 0)
	st, ok := s.Settings.StrategyByName(c.Strategy)
	if !ok {
		return Verified{}, ErrInvalidChallenge
	}
	h := handover{newUser: c.NewUser, profile: c.Profile, org: c.ViaOrg, strategy: c.Strategy, passkey: cred.ID, passkeySynced: cred.BackupEligible}
	out, err := s.nextStep(ctx, u, st, append(append([]string{}, doneOf(c)...), MethodPasskey), h)
	if err != nil {
		return Verified{}, err
	}
	return Verified{Redeemed: out, Factor: c.Factor, Method: MethodPasskey}, nil
}

// BeginPasskeyReauth gives the options for proving who the signed-in user is with a passkey.
func (s *Users) BeginPasskeyReauth(ctx context.Context, p Principal) ([]byte, error) {
	svc, _, err := s.stepService("reauth", p.Session.ID)
	if err != nil {
		return nil, err
	}
	options, _, err := svc.BeginLogin(ctx, p.User.ID)
	if errors.Is(err, passkey.ErrNoCredentials) {
		return nil, ErrReauthMethod
	}
	return options, err
}

// reauthPasskey checks a passkey as the proof, counting a wrong one against the user's lock and the
// address's throttle as a wrong code is.
func (s *Users) reauthPasskey(ctx context.Context, p Principal, credential []byte, ip string) error {
	ks := keys(s.Settings.Throttle(), "", ip)
	if err := s.checkThrottle(ctx, ks); err != nil {
		return err
	}
	release, err := s.lockLogin(ctx, mfaUserKey(p.User.ID))
	if err != nil {
		return err
	}
	defer release()
	if err := s.checkMFALock(ctx, p.User.ID); err != nil {
		return err
	}
	svc, id, err := s.stepService("reauth", p.Session.ID)
	if err != nil {
		return err
	}
	_, ok, err := s.verifyKnownPasskey(ctx, svc, id, p.User.ID, credential)
	if err != nil {
		return err
	}
	if !ok {
		now := s.now()
		if err := errors.Join(s.Store.RecordLoginFailure(ctx, mfaUserKey(p.User.ID), now, now.Add(mfaFailureWindow)), s.recordFailure(ctx, ks)); err != nil {
			return err
		}
		return &InvalidCodeError{AttemptsLeft: -1}
	}
	_ = s.Store.ClearLoginAttempts(ctx, mfaUserKey(p.User.ID))
	return nil
}

// credentialRequiredAfter says that, without the passkey, a holder of an admin role could complete no
// strategy those roles allow: the credential is what lets the administrator in.
func (s *Users) credentialRequiredAfter(ctx context.Context, u User, removing string) (bool, error) {
	// The roles the user holds, not only those the session keeps active: the question is who could sign in.
	if stored, err := s.Store.UserByID(ctx, u.ID); err == nil {
		u.Roles = stored.Roles
	}
	admin := false
	var allowed []string // names; nil with allAllowed
	allAllowed := false
	for _, r := range u.Roles {
		role, ok := s.Settings.Roles[r]
		if !ok || role.Admin == 0 {
			continue
		}
		admin = true
		if len(role.Strategies) == 0 {
			allAllowed = true
		}
		allowed = append(allowed, role.Strategies...)
	}
	if !admin || len(s.Settings.Strategies) == 0 {
		return false, nil
	}
	ids, err := s.Identities(ctx, u.ID)
	if err != nil {
		return false, err
	}
	list, err := s.Store.ListByUser(ctx, u.ID)
	if err != nil {
		return false, err
	}
	left := 0
	for _, c := range list {
		if c.ID != removing {
			left++
		}
	}
	usable := func(passkeys int) bool {
		for _, st := range s.Settings.Strategies {
			if !allAllowed && !containsString(allowed, st.Name) {
				continue
			}
			switch first := st.Methods[0]; first {
			case MethodPasskey:
				if passkeys > 0 {
					return true
				}
			case registry.MethodPassword:
				if hasIdentity(ids, ProviderPassword) {
					return true
				}
			case registry.MethodMagicLink:
				if s.magicLinkAvailable(u) {
					return true
				}
			case registry.MethodSSO:
				return true
			default:
				if hasIdentity(ids, first) {
					return true
				}
			}
		}
		return false
	}
	return usable(len(list)) && !usable(left), nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasIdentity(ids []Identity, provider string) bool {
	for _, i := range ids {
		if i.Provider == provider && i.Org == "" {
			return true
		}
	}
	return false
}
