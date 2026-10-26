package auth

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// Signing in to an organization (design/scoped-sessions.md §5): the person names the organization
// they are coming to, and the session they get is scoped to it. Every way of failing to get
// there looks like a wrong password, so nothing says who belongs where.

// orgIDPattern is what an organization id looks like (an xid). A slug may not look like one
// (validSlug), so a name can never be mistaken for another organization's id.
var orgIDPattern = regexp.MustCompile(`^[0-9a-v]{20}$`)

// LooksLikeOrgID says whether s has the shape of an organization id.
func LooksLikeOrgID(s string) bool { return orgIDPattern.MatchString(s) }

// OrgByRef finds the organization a person means by what they gave: its id or its slug (case
// and surrounding spaces do not matter). Only organizations a person can sign in to are found,
// active ones and orphaned ones (their members keep access); everything else is ErrNotFound.
func (s *Users) OrgByRef(ctx context.Context, ref string) (Org, error) {
	if s.Settings.Organizations == nil {
		return Org{}, ErrNotFound
	}
	ref = strings.TrimSpace(ref)
	var org Org
	var err error
	if LooksLikeOrgID(ref) {
		org, err = s.Store.OrgByID(ctx, ref)
	} else {
		org, err = s.Store.OrgBySlug(ctx, strings.ToLower(ref))
	}
	if err != nil {
		return Org{}, err
	}
	if org.Status != OrgActive && org.Status != OrgOrphaned {
		return Org{}, ErrNotFound
	}
	return org, nil
}

// OrgSignIn is a password sign-in to an organization.
type OrgSignIn struct {
	// Org is the organization's id or slug.
	Org, Email, Password, IP string
	// Invitation is an organization invitation token to use as part of the sign-in; with it a
	// person who is not a member yet can come in, and joins.
	Invitation string
}

// SignInToOrg checks an email and password for an organization: the person must be one of its
// members, or hold a valid invitation to it. A wrong password, an unknown address, an unknown or
// inactive organization and a person who does not belong are all ErrInvalidCredentials after the
// same work, and count the same toward the sign-in limits. The session, or the second step, is
// scoped to the organization.
func (s *Users) SignInToOrg(ctx context.Context, in OrgSignIn) (Redeemed, error) {
	org, orgErr := s.OrgByRef(ctx, in.Org)
	gate := func(u User) error {
		if orgErr != nil {
			return ErrInvalidCredentials
		}
		_, err := s.Store.OrgMember(ctx, org.ID, u.ID)
		switch {
		case err == nil:
		case errors.Is(err, ErrNotFound):
			if in.Invitation == "" {
				return ErrInvalidCredentials
			}
		default:
			return err
		}
		if in.Invitation != "" {
			// A token that is not for this person and this organization is a failed attempt, so the
			// answer to a right password does not depend on it.
			pv, err := s.lookupOrgInvitation(ctx, u, in.Invitation)
			if err != nil || pv.Org.ID != org.ID {
				return ErrInvalidCredentials
			}
		}
		return nil
	}
	after := func(u User) error {
		if in.Invitation == "" {
			return nil
		}
		_, err := s.acceptOrgInvitation(ctx, u, in.Invitation)
		return err
	}
	var h handover
	if orgErr == nil {
		h.org = org.ID
	}
	return s.signIn(ctx, in.Email, in.Password, in.IP, signInHooks{Gate: gate, After: after, Handover: h})
}

// OrgSignupRequest is a sign-up to an organization: the person is let in by an invitation.
type OrgSignupRequest struct {
	SignupRequest
	// Org is the organization's id or slug. Invitation (of SignupRequest) is its invitation token.
	Org string
}

// SignUpToOrg creates an account for someone an organization invited and signs them in to it.
// Whatever is wrong with the invitation, the organization or the address it is bound to, the
// answer is ErrInvalidOrgInvitation or ErrInvitationNotForYou, after the token has proved
// itself. An invitation bound to the address was sent to it, so the account is born verified.
func (s *Users) SignUpToOrg(ctx context.Context, r OrgSignupRequest) (SignupResult, error) {
	pv, err := s.lookupOrgInvitationToken(ctx, r.Invitation)
	if err != nil {
		return SignupResult{}, err
	}
	org, err := s.OrgByRef(ctx, r.Org)
	if err != nil || org.ID != pv.Org.ID {
		return SignupResult{}, ErrInvalidOrgInvitation
	}
	address, err := registry.NormalizeEmail(r.Email)
	if err != nil {
		return SignupResult{}, err
	}
	inv := pv.invitation
	if inv.Binding == BindUser || (inv.Binding == BindEmail && inv.Email != address) {
		return SignupResult{}, ErrInvitationNotForYou
	}
	if err := s.checkSignupConsents(r.Consents); err != nil {
		return SignupResult{}, err
	}
	verified := inv.Binding == BindEmail
	locale := s.Settings.BestLocale(r.Locales...)
	u, err := s.create(ctx, address, &r.Password, locale, verified)
	if errors.Is(err, ErrEmailTaken) && s.Settings.Account.RequireVerifiedEmail {
		// The same answer as for a new address; the owner is told by email.
		s.sendAccountExists(ctx, r.SignupRequest)
		return SignupResult{Pending: true}, nil
	}
	if err != nil {
		return SignupResult{}, err
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditUserSignup, userTarget(u.ID), map[string]any{"invited": true, "org": org.ID, "roles": u.Roles})
	s.queueOnSignup(ctx, u, ProviderPassword, nil)
	if _, err := s.useOrgInvitation(ctx, u, pv); err != nil {
		// The invitation was used up by someone else a moment ago: nothing of this sign-up stays.
		_ = s.Store.DeleteUser(context.WithoutCancel(ctx), u.ID)
		return SignupResult{}, err
	}
	return s.finishOrgSignup(ctx, u, r.SignupRequest, org.ID)
}

// finishOrgSignup is what follows the creation of an account that has an organization: the
// consents, the verification email, and the session (or none, while the realm wants the address
// verified first).
func (s *Users) finishOrgSignup(ctx context.Context, u User, r SignupRequest, orgID string) (SignupResult, error) {
	if len(r.Consents) > 0 {
		if err := s.recordGiven(ctx, u.ID, r.Consents, ConsentMethodSignup, ConsentContext{IP: r.ClientIP, UserAgent: r.UserAgent}, u.Locale); err != nil {
			return SignupResult{}, err
		}
		u.Consents = mergeConsents(u.Consents, r.Consents)
	}
	if !u.EmailVerified {
		s.sendVerification(ctx, u, r.RedirectTo, r.ClientIP, r.RequestID)
	}
	if s.Settings.Account.RequireVerifiedEmail && !u.EmailVerified {
		return SignupResult{Pending: true}, nil
	}
	out, err := s.afterFirstFactor(ctx, u, ProviderPassword, handover{org: orgID})
	if err != nil {
		return SignupResult{}, err
	}
	return SignupResult{Principal: out.Principal, Token: out.Token, Challenge: out.Challenge}, nil
}

// NewOrgSignupRequest is a new person creating an account and their organization.
type NewOrgSignupRequest struct {
	SignupRequest
	// Name and Slug are the organization's; the slug is required.
	Name, Slug string
}

// SignUpWithOrg makes an account and an organization for someone new, with the person as its
// creator, and signs them in to it. The realm's sign-up decides who may: anyone when it is open,
// whoever holds an invitation of the realm (SignupRequest.Invitation) when it is invite-only, and
// nobody when it is closed. This is how a company starts, and organizations.create does not apply
// to the first one (a new account cannot satisfy user.email_verified); max_created_per_user does.
// The organization is returned when it was made;
// the answer for a registered address, when the realm verifies addresses, is the same as for a
// new one and makes nothing.
func (s *Users) SignUpWithOrg(ctx context.Context, r NewOrgSignupRequest) (SignupResult, Membership, error) {
	o, err := s.orgs()
	if err != nil {
		return SignupResult{}, Membership{}, err
	}
	name, err := validOrgName(r.Name)
	if err != nil {
		return SignupResult{}, Membership{}, err
	}
	if r.Slug == "" {
		return SignupResult{}, Membership{}, &OrgInvalidError{"slug", "is required"}
	}
	if err := validSlug(r.Slug); err != nil {
		return SignupResult{}, Membership{}, err
	}
	if _, err := s.Store.OrgBySlug(ctx, r.Slug); err == nil {
		return SignupResult{}, Membership{}, ErrSlugTaken
	} else if !errors.Is(err, ErrNotFound) {
		return SignupResult{}, Membership{}, err
	}
	if err := s.checkSignupConsents(r.Consents); err != nil {
		return SignupResult{}, Membership{}, err
	}
	u, err := s.signup(ctx, r.Email, r.Password, r.Invitation, append([]string{}, r.Locales...))
	if errors.Is(err, ErrEmailTaken) && s.Settings.Account.RequireVerifiedEmail {
		s.sendAccountExists(ctx, r.SignupRequest)
		return SignupResult{Pending: true}, Membership{}, nil
	}
	if err != nil {
		return SignupResult{}, Membership{}, err
	}
	m, err := s.createOrg(ctx, o, name, r.Slug, u.ID, "signup")
	if err != nil {
		// Nothing of this sign-up stays: the person can try again with another name.
		_ = s.Store.DeleteUser(context.WithoutCancel(ctx), u.ID)
		return SignupResult{}, Membership{}, err
	}
	res, err := s.finishOrgSignup(ctx, u, r.SignupRequest, m.Org.ID)
	return res, m, err
}

// OpenOrgSession gives a signed-in person a second session, scoped to an organization they
// belong to: the one a person gets after creating another organization. It carries how the
// caller proved themselves.
func (s *Users) OpenOrgSession(ctx context.Context, p Principal, orgID string) (Principal, string, error) {
	if _, err := s.MembershipOf(ctx, p.User.ID, orgID); err != nil {
		return Principal{}, "", err
	}
	return s.startSessionWith(ctx, p.User, slices.Clone(p.Session.Methods), p.Session.MFA, orgID)
}

// The "which companies am I in?" email (design/scoped-sessions.md §5.4).
const (
	// FindOrgsPerAddress and FindOrgsPerIP are how many times an hour an address, and a client, may
	// ask for it.
	FindOrgsPerAddress = 3
	FindOrgsPerIP      = 30
)

// OrgSignInLink is an organization of a person, with where its sign-in page is.
type OrgSignInLink struct {
	ID, Slug, Name, Link string
}

// FindOrgs queues the email that tells the owner of an address which organizations it belongs
// to, with a link to each one's sign-in page. It answers nothing about the address: one with no
// account, one without memberships and one that is sent the email all do the same work and end
// the same way. It needs the realm's organizations.signin_link and email.
func (s *Users) FindOrgs(ctx context.Context, address, locale, ip, requestID string) error {
	o, err := s.orgs()
	if err != nil {
		return err
	}
	if o.SigninLink == "" || s.Settings.Email == nil {
		return ErrNotFound
	}
	norm, err := registry.NormalizeEmail(address)
	if err != nil {
		return err
	}
	if err := s.RateLimit(ctx, "find-orgs:"+norm, FindOrgsPerAddress, time.Hour); err != nil {
		return err
	}
	if err := s.RateLimit(ctx, "find-orgs-ip:"+ip, FindOrgsPerIP, time.Hour); err != nil {
		return err
	}
	u, err := s.Store.UserByEmail(ctx, norm)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.Disabled || !u.ErasedAt.IsZero() {
		return nil
	}
	links, err := s.SignInLinks(ctx, u.ID)
	if err != nil || len(links) == 0 {
		return err
	}
	if locale == "" {
		locale = s.LocaleOf(u)
	}
	_, err = s.QueueEmail(ctx, EmailRequest{Kind: email.OrganizationsFound, UserID: u.ID, Locale: locale, ClientIP: ip, RequestID: requestID, Notice: true})
	if _, ok := errors.AsType[*EmailLimitedError](err); ok {
		return nil
	}
	return err
}

// SignInLinks lists the organizations a user can sign in to, by name, each with the link to its
// sign-in page (organizations.signin_link with the slug, or the id when it has none, and the id).
func (s *Users) SignInLinks(ctx context.Context, userID string) ([]OrgSignInLink, error) {
	o, err := s.orgs()
	if err != nil {
		return nil, err
	}
	members, err := s.Store.OrgMembershipsOf(ctx, userID, o.MaxMembershipsPerUser)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.Org
	}
	found, err := s.Store.OrgsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []OrgSignInLink
	for _, m := range members {
		org, ok := found[m.Org]
		if !ok || (org.Status != OrgActive && org.Status != OrgOrphaned) {
			continue
		}
		slug := org.Slug
		if slug == "" {
			slug = org.ID
		}
		link := strings.NewReplacer("{slug}", slug, "{org}", org.ID).Replace(o.SigninLink)
		out = append(out, OrgSignInLink{ID: org.ID, Slug: org.Slug, Name: org.Name, Link: link})
	}
	slices.SortFunc(out, func(a, b OrgSignInLink) int {
		return strings.Compare(strings.ToLower(a.Name)+a.ID, strings.ToLower(b.Name)+b.ID)
	})
	return out, nil
}
