package sso

// Action is what Decide says to do with a login.
type Action int

// The actions.
const (
	// Refuse ends the login with Decision.Err.
	Refuse Action = iota
	// SignInKnown: the identity is linked already; sign in as its user.
	SignInKnown
	// LinkToSignedIn adds the identity to the signed-in user (IntentLink).
	LinkToSignedIn
	// LinkToExisting adds the identity to the account that has the provider's address and
	// signs in as it.
	LinkToExisting
	// Create makes a new user and the identity.
	Create
)

// Facts are what the host found out about a login.
type Facts struct {
	Intent Intent
	// Known is the owner of the identity, when it is linked.
	Known *Account
	// LinkUser is the signed-in user of an IntentLink attempt (nil when there is none to find).
	LinkUser *Account
	// ByEmail is the account that has the provider's address.
	ByEmail *Account
	// Email is the provider's address, normalized; empty when it gave none or an invalid one.
	Email string
	// EmailVerified is the provider's word on it.
	EmailVerified bool
	// LinkUserID is the user an IntentLink attempt was started by.
	LinkUserID string
}

// Decision is the outcome of Decide.
type Decision struct {
	Action Action
	// Err is set with Refuse.
	Err error
	// Verified, with Create: the new user's address counts as verified (the provider is trusted
	// and says it is).
	Verified bool
}

// Decide is the rule that turns a login into an action (design/sso.md §8.1). It reads nothing
// and changes nothing: the host supplies the facts and carries the action out.
func Decide(p Provider, f Facts) Decision {
	refuse := func(err error) Decision { return Decision{Action: Refuse, Err: err} }
	if f.Known != nil {
		if f.Intent == IntentLink && f.Known.ID != f.LinkUserID {
			return refuse(ErrLinkConflict)
		}
		return Decision{Action: SignInKnown}
	}
	if f.Intent == IntentLink {
		if f.LinkUser == nil || f.LinkUser.Disabled || f.LinkUser.Erased {
			return refuse(&Refused{UserID: f.LinkUserID, Reason: "link to an unusable user"})
		}
		return Decision{Action: LinkToSignedIn}
	}
	if f.Email == "" {
		return refuse(ErrEmailRequired)
	}
	trusted := p.Link == LinkVerifiedEmail
	if f.ByEmail != nil {
		if p.Org != "" || !trusted || !f.EmailVerified || !f.ByEmail.EmailVerified {
			return refuse(ErrAccountExists)
		}
		return Decision{Action: LinkToExisting}
	}
	return Decision{Action: Create, Verified: trusted && f.EmailVerified}
}
