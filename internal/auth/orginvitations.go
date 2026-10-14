package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// Organization invitations (design/organizations.md §7): a way to join an organization with a
// link, bound to a user, to an address or to nobody.

// OrgInvitationPrefix starts every organization invitation token.
const OrgInvitationPrefix = "bdo_"

// Bindings of an invitation: who may accept it.
const (
	BindUser  = "user"  // only that user
	BindEmail = "email" // the signed-in user whose verified address matches
	BindNone  = "none"  // anyone signed in who holds the link
)

// Audit actions of invitations; the target is org:<id>, details never hold a token.
const (
	AuditOrgInviteCreate = "org.invitation_create" // details: invitation, binding, roles, teams, sent
	AuditOrgInviteLink   = "org.invitation_link"   // a new link was issued; details: invitation
	AuditOrgInviteAccept = "org.invitation_accept" // details: invitation, user
	AuditOrgInviteRevoke = "org.invitation_revoke" // details: invitation
)

var (
	// ErrInvalidOrgInvitation covers unknown, expired, revoked and used-up tokens alike.
	ErrInvalidOrgInvitation = errors.New("invalid or expired invitation")
	// ErrInvitationNotForYou: the token is valid but bound to someone else.
	ErrInvitationNotForYou = errors.New("this invitation is not for you")
	// ErrInvitationsLimit: the organization has the most pending invitations it may.
	ErrInvitationsLimit = errors.New("the organization has the most pending invitations it may")
	// ErrNeedsSecretsKey: sending the link by email keeps it sealed with the instance's key.
	ErrNeedsSecretsKey = errors.New("sending an invitation by email needs the instance's secrets key (BACKD_SECRETS_KEY)")
)

// OrgInvitation is an invitation to an organization. Only a hash of the token is stored, plus
// the token sealed with the realm's key for as long as an email that carries it may be sent.
type OrgInvitation struct {
	ID          string
	Org         string
	TokenHash   string
	SealedToken string // set only when an email carries the link
	Binding     string
	User        string // BindUser
	Email       string // BindEmail
	Roles       []string
	Teams       []string
	MaxUses     int
	Uses        int
	CreatedBy   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RevokedAt   time.Time
}

// Status of an invitation at now: pending, used (up), revoked or expired.
func (i OrgInvitation) Status(now time.Time) string {
	switch {
	case !i.RevokedAt.IsZero():
		return "revoked"
	case !now.Before(i.ExpiresAt):
		return "expired"
	case i.MaxUses > 0 && i.Uses >= i.MaxUses:
		return "used"
	}
	return "pending"
}

// OrgTeam is a group of members of an organization that carries organization roles.
type OrgTeam struct {
	ID        string
	Org       string
	Name      string
	Roles     []string
	CreatedAt time.Time
}

// InvitationInput is a request to invite.
type InvitationInput struct {
	Binding string
	User    string
	Email   string
	Roles   []string
	Teams   []string
	Send    bool
	TTL     time.Duration // 0: the default
	MaxUses int           // BindNone; 0: 1
	Locale  string
	IP      string
	Request string
}

// InvitationResult is what creating or renewing an invitation answers: the link is shown only
// here.
type InvitationResult struct {
	Invitation OrgInvitation
	Token      string
	Link       string // "" when the realm sets no invitation_link
}

// MaxInvitationUses is the most an open link may be used.
const MaxInvitationUses = 1000

func newOrgInvitationToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return OrgInvitationPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// teamRoles returns the union of the roles of the teams, and the teams that exist.
func (s *Users) teamRoles(ctx context.Context, org string, ids []string) (roles []string, found []OrgTeam, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	found, err = s.Store.OrgTeamsByIDs(ctx, org, ids)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range found {
		for _, r := range t.Roles {
			if !slices.Contains(roles, r) {
				roles = append(roles, r)
			}
		}
	}
	return roles, found, nil
}

// CreateOrgInvitation invites to an organization; it needs manage: members, and gives only
// what the caller may give.
func (s *Users) CreateOrgInvitation(ctx context.Context, userID, orgID string, in InvitationInput) (InvitationResult, error) {
	caller, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return InvitationResult{}, err
	}
	cc := s.caps(caller)
	if !cc.Has(registry.CapMembers) {
		return InvitationResult{}, ErrOrgForbidden
	}
	return s.createOrgInvitation(ctx, caller.Org, cc, userID, in)
}

// AdminCreateOrgInvitation is CreateOrgInvitation for an administrator, who may give any role.
func (s *Users) AdminCreateOrgInvitation(ctx context.Context, orgID string, in InvitationInput) (InvitationResult, error) {
	if _, err := s.orgs(); err != nil {
		return InvitationResult{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return InvitationResult{}, err
	}
	if org.Status == OrgDeleted {
		return InvitationResult{}, ErrOrgNotActive
	}
	return s.createOrgInvitation(ctx, org, registry.AllCaps, "", in)
}

func (s *Users) createOrgInvitation(ctx context.Context, org Org, callerCaps registry.OrgCaps, by string, in InvitationInput) (InvitationResult, error) {
	o := s.Settings.Organizations
	inv := OrgInvitation{ID: xid.New().String(), Org: org.ID, Binding: in.Binding, CreatedBy: by}
	switch in.Binding {
	case BindUser:
		if in.User == "" {
			return InvitationResult{}, &OrgInvalidError{"user", "is required for a binding to a user"}
		}
		if err := s.userExists(ctx, in.User); err != nil {
			return InvitationResult{}, &OrgInvalidError{"user", "is not a user of this realm"}
		}
		inv.User = in.User
	case BindEmail:
		address, err := registry.NormalizeEmail(in.Email)
		if err != nil {
			return InvitationResult{}, &OrgInvalidError{"email", err.Error()}
		}
		inv.Email = address
	case BindNone:
		inv.MaxUses = in.MaxUses
		if inv.MaxUses == 0 {
			inv.MaxUses = 1
		}
		if inv.MaxUses < 1 || inv.MaxUses > MaxInvitationUses {
			return InvitationResult{}, &OrgInvalidError{"max_uses", fmt.Sprintf("must be between 1 and %d", MaxInvitationUses)}
		}
	default:
		return InvitationResult{}, &OrgInvalidError{"binding", "must be user, email or none"}
	}
	if in.Binding != BindNone && in.MaxUses != 0 {
		return InvitationResult{}, &OrgInvalidError{"max_uses", "only an open link (binding none) has uses"}
	}
	if in.Binding != BindNone {
		inv.MaxUses = 1
	}
	if in.Send && o.InvitationLink == "" {
		return InvitationResult{}, &OrgInvalidError{"send", "needs organizations.invitation_link in realm.yaml: the email carries that link"}
	}
	if in.Send && in.Binding != BindEmail {
		return InvitationResult{}, &OrgInvalidError{"send", "only an invitation bound to an email address can be sent"}
	}
	ttl := in.TTL
	limit := registry.MaxOrgInvitationTTL
	switch {
	case ttl == 0:
		ttl = registry.DefaultOrgInvitationTTL
	case ttl < time.Minute || ttl > limit:
		return InvitationResult{}, &OrgInvalidError{"expires_in", "must be between 1m and 30d"}
	}
	// Roles and teams follow the granting rules; an open link never gives a capability.
	roles := uniqueStrings(in.Roles)
	teamRoles, teams, err := s.teamRoles(ctx, org.ID, uniqueStrings(in.Teams))
	if err != nil {
		return InvitationResult{}, err
	}
	if len(teams) != len(uniqueStrings(in.Teams)) {
		return InvitationResult{}, &OrgInvalidError{"teams", "names a team that doesn't exist in this organization"}
	}
	all := append(slices.Clone(roles), teamRoles...)
	if err := s.checkGrant(callerCaps, all); err != nil {
		return InvitationResult{}, err
	}
	if in.Binding == BindNone && s.capsOfRoles(all).Any() {
		return InvitationResult{}, &OrgInvalidError{"roles", "an open link can't give a role that manages the organization, directly or through a team"}
	}
	inv.Roles = roles
	for _, t := range teams {
		inv.Teams = append(inv.Teams, t.ID)
	}
	n, err := s.Store.CountPendingOrgInvitations(ctx, org.ID, s.now())
	if err != nil {
		return InvitationResult{}, err
	}
	if n >= o.MaxPendingInvitations {
		return InvitationResult{}, ErrInvitationsLimit
	}
	token, err := newOrgInvitationToken()
	if err != nil {
		return InvitationResult{}, err
	}
	now := s.now()
	inv.TokenHash, inv.CreatedAt, inv.ExpiresAt = HashToken(token), now, now.Add(ttl)
	if in.Send {
		if s.Settings.Email == nil {
			return InvitationResult{}, ErrEmailNotConfigured
		}
		if inv.SealedToken, err = s.SealToken(token); err != nil {
			return InvitationResult{}, ErrNeedsSecretsKey
		}
	}
	if err := s.Store.CreateOrgInvitation(ctx, inv); err != nil {
		return InvitationResult{}, err
	}
	if in.Send {
		if err := s.queueOrgInvitationEmail(ctx, inv, in.Locale, in.IP, in.Request); err != nil {
			_ = s.Store.DeleteOrgInvitation(context.WithoutCancel(ctx), org.ID, inv.ID)
			return InvitationResult{}, err
		}
	}
	s.Audit(ctx, AuditOrgInviteCreate, orgTarget(org.ID), map[string]any{
		"invitation": inv.ID, "binding": inv.Binding, "roles": inv.Roles, "teams": inv.Teams, "sent": in.Send,
	})
	return InvitationResult{Invitation: inv, Token: token, Link: s.invitationLink(token, org.ID)}, nil
}

func (s *Users) invitationLink(token, org string) string {
	o := s.Settings.Organizations
	if o == nil || o.InvitationLink == "" {
		return ""
	}
	return o.InvitationURL(token, org)
}

func (s *Users) queueOrgInvitationEmail(ctx context.Context, inv OrgInvitation, locale, ip, requestID string) error {
	_, err := s.QueueEmail(ctx, EmailRequest{
		Kind: email.OrgInvitation, OrgInvitationID: inv.Org + "/" + inv.ID, Address: inv.Email,
		Locale: s.Settings.BestLocale(locale), ClientIP: ip, RequestID: requestID,
	})
	return err
}

// OrgInvitations lists an organization's invitations, newest first, to someone who may
// manage members. Tokens are never shown.
func (s *Users) OrgInvitations(ctx context.Context, userID, orgID string) ([]OrgInvitation, error) {
	caller, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return nil, err
	}
	if !s.caps(caller).Has(registry.CapMembers) {
		return nil, ErrOrgForbidden
	}
	return s.Store.ListOrgInvitations(ctx, orgID)
}

// AdminOrgInvitations lists an organization's invitations.
func (s *Users) AdminOrgInvitations(ctx context.Context, orgID string) ([]OrgInvitation, error) {
	if _, err := s.orgs(); err != nil {
		return nil, err
	}
	if _, err := s.Store.OrgByID(ctx, orgID); err != nil {
		return nil, err
	}
	return s.Store.ListOrgInvitations(ctx, orgID)
}

// managedInvitation returns an invitation of the organization the caller may manage: the
// caller needs manage: members and must cover what the invitation gives.
func (s *Users) managedInvitation(ctx context.Context, userID, orgID, id string) (OrgInvitation, Org, registry.OrgCaps, error) {
	caller, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return OrgInvitation{}, Org{}, 0, err
	}
	cc := s.caps(caller)
	if !cc.Has(registry.CapMembers) {
		return OrgInvitation{}, Org{}, 0, ErrOrgForbidden
	}
	inv, err := s.Store.OrgInvitationByID(ctx, orgID, id)
	if err != nil {
		return OrgInvitation{}, Org{}, 0, err
	}
	return inv, caller.Org, cc, nil
}

// RevokeOrgInvitation ends an invitation.
func (s *Users) RevokeOrgInvitation(ctx context.Context, userID, orgID, id string) error {
	if _, _, _, err := s.managedInvitation(ctx, userID, orgID, id); err != nil {
		return err
	}
	return s.revokeOrgInvitation(ctx, orgID, id)
}

// AdminRevokeOrgInvitation is RevokeOrgInvitation for an administrator.
func (s *Users) AdminRevokeOrgInvitation(ctx context.Context, orgID, id string) error {
	if _, err := s.Store.OrgInvitationByID(ctx, orgID, id); err != nil {
		return err
	}
	return s.revokeOrgInvitation(ctx, orgID, id)
}

func (s *Users) revokeOrgInvitation(ctx context.Context, orgID, id string) error {
	if err := s.Store.RevokeOrgInvitation(ctx, orgID, id, s.now()); err != nil {
		return err
	}
	s.Audit(ctx, AuditOrgInviteRevoke, orgTarget(orgID), map[string]any{"invitation": id})
	return nil
}

// NewOrgInvitationLink issues a new token for an invitation that is still pending and
// invalidates the old one: a lost link can't be shown again.
func (s *Users) NewOrgInvitationLink(ctx context.Context, userID, orgID, id string) (InvitationResult, error) {
	inv, org, _, err := s.managedInvitation(ctx, userID, orgID, id)
	if err != nil {
		return InvitationResult{}, err
	}
	return s.renewOrgInvitation(ctx, inv, org.ID)
}

// AdminNewOrgInvitationLink is NewOrgInvitationLink for an administrator.
func (s *Users) AdminNewOrgInvitationLink(ctx context.Context, orgID, id string) (InvitationResult, error) {
	inv, err := s.Store.OrgInvitationByID(ctx, orgID, id)
	if err != nil {
		return InvitationResult{}, err
	}
	return s.renewOrgInvitation(ctx, inv, orgID)
}

func (s *Users) renewOrgInvitation(ctx context.Context, inv OrgInvitation, orgID string) (InvitationResult, error) {
	if inv.Status(s.now()) != "pending" {
		return InvitationResult{}, ErrInvalidOrgInvitation
	}
	token, err := newOrgInvitationToken()
	if err != nil {
		return InvitationResult{}, err
	}
	sealed := ""
	if inv.SealedToken != "" { // an email may still carry it
		if sealed, err = s.SealToken(token); err != nil {
			return InvitationResult{}, ErrNeedsSecretsKey
		}
	}
	if err := s.Store.ReplaceOrgInvitationToken(ctx, orgID, inv.ID, HashToken(token), sealed); err != nil {
		return InvitationResult{}, err
	}
	s.Audit(ctx, AuditOrgInviteLink, orgTarget(orgID), map[string]any{"invitation": inv.ID})
	inv.TokenHash, inv.SealedToken = HashToken(token), sealed
	return InvitationResult{Invitation: inv, Token: token, Link: s.invitationLink(token, orgID)}, nil
}

// ResendOrgInvitation sends the email of an invitation bound to an address again.
func (s *Users) ResendOrgInvitation(ctx context.Context, userID, orgID, id, locale, ip, requestID string) error {
	inv, _, _, err := s.managedInvitation(ctx, userID, orgID, id)
	if err != nil {
		return err
	}
	return s.resendOrgInvitation(ctx, inv, locale, ip, requestID)
}

// AdminResendOrgInvitation is ResendOrgInvitation for an administrator.
func (s *Users) AdminResendOrgInvitation(ctx context.Context, orgID, id, locale, ip, requestID string) error {
	inv, err := s.Store.OrgInvitationByID(ctx, orgID, id)
	if err != nil {
		return err
	}
	return s.resendOrgInvitation(ctx, inv, locale, ip, requestID)
}

func (s *Users) resendOrgInvitation(ctx context.Context, inv OrgInvitation, locale, ip, requestID string) error {
	if inv.Binding != BindEmail {
		return &OrgInvalidError{"binding", "only an invitation bound to an email address can be sent"}
	}
	if inv.Status(s.now()) != "pending" {
		return ErrInvalidOrgInvitation
	}
	if inv.SealedToken == "" {
		// The link was never sent: seal a fresh token for this email.
		token, err := newOrgInvitationToken()
		if err != nil {
			return err
		}
		sealed, err := s.SealToken(token)
		if err != nil {
			return ErrNeedsSecretsKey
		}
		if err := s.Store.ReplaceOrgInvitationToken(ctx, inv.Org, inv.ID, HashToken(token), sealed); err != nil {
			return err
		}
	}
	return s.queueOrgInvitationEmail(ctx, inv, locale, ip, requestID)
}

// OrgInvitationPreview is what someone is shown before accepting.
type OrgInvitationPreview struct {
	Org        Org
	Roles      []string
	Teams      []OrgTeam
	Binding    string
	ExpiresAt  time.Time
	invitation OrgInvitation
}

// lookupOrgInvitation finds the invitation of a token for a signed-in user. Unknown, expired,
// revoked and used-up tokens are all ErrInvalidOrgInvitation; a binding that doesn't fit is
// ErrInvitationNotForYou, said only once the token has proved valid.
func (s *Users) lookupOrgInvitation(ctx context.Context, p Principal, token string) (OrgInvitationPreview, error) {
	if _, err := s.orgs(); err != nil {
		return OrgInvitationPreview{}, err
	}
	if !strings.HasPrefix(token, OrgInvitationPrefix) {
		return OrgInvitationPreview{}, ErrInvalidOrgInvitation
	}
	inv, err := s.Store.OrgInvitationByHash(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		return OrgInvitationPreview{}, ErrInvalidOrgInvitation
	}
	if err != nil {
		return OrgInvitationPreview{}, err
	}
	if inv.Status(s.now()) != "pending" {
		return OrgInvitationPreview{}, ErrInvalidOrgInvitation
	}
	org, err := s.Store.OrgByID(ctx, inv.Org)
	if errors.Is(err, ErrNotFound) || (err == nil && org.Status == OrgDeleted) {
		return OrgInvitationPreview{}, ErrInvalidOrgInvitation
	}
	if err != nil {
		return OrgInvitationPreview{}, err
	}
	switch inv.Binding {
	case BindUser:
		if p.User.ID != inv.User {
			return OrgInvitationPreview{}, ErrInvitationNotForYou
		}
	case BindEmail:
		if !p.User.EmailVerified || p.User.Email != inv.Email {
			return OrgInvitationPreview{}, ErrInvitationNotForYou
		}
	}
	_, teams, err := s.teamRoles(ctx, inv.Org, inv.Teams)
	if err != nil {
		return OrgInvitationPreview{}, err
	}
	return OrgInvitationPreview{Org: org, Roles: inv.Roles, Teams: teams, Binding: inv.Binding, ExpiresAt: inv.ExpiresAt, invitation: inv}, nil
}

// PreviewOrgInvitation says what accepting a token would do, without doing it.
func (s *Users) PreviewOrgInvitation(ctx context.Context, p Principal, token string) (OrgInvitationPreview, error) {
	return s.lookupOrgInvitation(ctx, p, token)
}

// AcceptOrgInvitation uses the token: the user becomes a member, or gains the roles and teams
// of the invitation. It returns the membership.
func (s *Users) AcceptOrgInvitation(ctx context.Context, p Principal, token string) (Membership, error) {
	pv, err := s.lookupOrgInvitation(ctx, p, token)
	if err != nil {
		return Membership{}, err
	}
	o := s.Settings.Organizations
	inv := pv.invitation
	existing, err := s.Store.OrgMember(ctx, inv.Org, p.User.ID)
	isMember := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Membership{}, err
	}
	if !isMember {
		if err := s.checkMembershipCap(ctx, o, p.User.ID); err != nil {
			return Membership{}, err
		}
	}
	// The use is taken atomically: two people racing for the last use can't both win.
	used, err := s.Store.UseOrgInvitation(ctx, inv.ID, s.now())
	if errors.Is(err, ErrNotFound) {
		return Membership{}, ErrInvalidOrgInvitation
	}
	if err != nil {
		return Membership{}, err
	}
	now := s.now()
	m := existing
	if !isMember {
		m = OrgMember{Org: inv.Org, User: p.User.ID, JoinedAt: now, InvitedBy: used.CreatedBy}
	}
	have := m.DirectRoles(now)
	for _, r := range used.Roles {
		if o.Declared(r) && !slices.Contains(have, r) {
			m.Roles = append(m.Roles, RoleGrant{Role: r, GrantedBy: used.CreatedBy, GrantedAt: now})
		}
	}
	for _, t := range pv.Teams { // the teams that still exist
		if !slices.Contains(m.Teams, t.ID) {
			m.Teams = append(m.Teams, t.ID)
		}
	}
	if err := s.Store.PutOrgMember(ctx, m); err != nil {
		return Membership{}, err
	}
	s.Audit(ctx, AuditOrgInviteAccept, orgTarget(inv.Org), map[string]any{"invitation": inv.ID, "user": p.User.ID, "roles": used.Roles, "teams": m.Teams})
	return s.membership(ctx, pv.Org, m)
}

// OrgInvitationEmail is what the worker needs to write the message of an invitation.
type OrgInvitationEmail struct {
	Address   string
	OrgName   string
	Roles     []string
	Token     string
	Link      string
	ExpiresAt time.Time
}

// ErrOrgInvitationGone means the invitation ended (revoked, used up, expired or its
// organization deleted) before its email was written: nothing is sent.
var ErrOrgInvitationGone = errors.New("the invitation ended before its email was sent")

// OrgInvitationEmail reads the invitation an email job names ("<org>/<id>") and opens its
// sealed token.
func (s *Users) OrgInvitationEmail(ctx context.Context, ref string) (OrgInvitationEmail, error) {
	orgID, id, ok := strings.Cut(ref, "/")
	if !ok {
		return OrgInvitationEmail{}, ErrOrgInvitationGone
	}
	inv, err := s.Store.OrgInvitationByID(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return OrgInvitationEmail{}, ErrOrgInvitationGone
	}
	if err != nil {
		return OrgInvitationEmail{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if errors.Is(err, ErrNotFound) || (err == nil && org.Status == OrgDeleted) || inv.Status(s.now()) != "pending" || inv.SealedToken == "" {
		return OrgInvitationEmail{}, ErrOrgInvitationGone
	}
	if err != nil {
		return OrgInvitationEmail{}, err
	}
	token, err := s.OpenToken(inv.SealedToken)
	if err != nil {
		return OrgInvitationEmail{}, err
	}
	return OrgInvitationEmail{Address: inv.Email, OrgName: org.Name, Roles: inv.Roles, Token: token, Link: s.invitationLink(token, orgID), ExpiresAt: inv.ExpiresAt}, nil
}
