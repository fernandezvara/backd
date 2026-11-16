package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// Verified domains (design/sso.md §10): a domain an organization has proved it controls by
// publishing a DNS record that backd checks. The proof is what lets an organization say "these
// addresses are ours" to other features; this file is the claim, the check and its upkeep.

// The states of a domain claim.
const (
	DomainPending  = "pending"  // the challenge is issued, the record not seen yet
	DomainVerified = "verified" // the record was seen, and is seen again every recheck
	DomainLapsed   = "lapsed"   // it was verified and the record is gone
)

// Audit actions of verified domains; the target is org:<id>. Details never hold the challenge.
const (
	AuditOrgDomainAdded    = "org.domain.added"    // details: domain
	AuditOrgDomainVerified = "org.domain.verified" // details: domain, again (it was lapsed)
	AuditOrgDomainLapsed   = "org.domain.lapsed"   // details: domain
	AuditOrgDomainRemoved  = "org.domain.removed"  // details: domain, by
	AuditOrgDomainSettings = "org.domain.settings" // details: domain, discoverable, link_accounts
)

var (
	// ErrDomainsDisabled: the realm doesn't let organizations prove domains (organizations.domains).
	ErrDomainsDisabled = errors.New("verified domains are not enabled in this realm")
	// ErrDomainNotFound: the organization has no claim on that domain.
	ErrDomainNotFound = errors.New("the organization has no claim on that domain")
	// ErrDomainTaken: another organization has verified the domain.
	ErrDomainTaken = errors.New("another organization has verified that domain")
	// ErrDomainsLimit: the organization has the most claims it may.
	ErrDomainsLimit = errors.New("the organization has the most domains it may claim")
	// ErrDomainExists: the organization already claimed the domain.
	ErrDomainExists = errors.New("the organization already claimed that domain")
)

// DomainRefusedError is a domain nobody may claim: free mail, a public suffix, blocked by the
// realm or not a domain at all.
type DomainRefusedError struct{ Reason string }

func (e *DomainRefusedError) Error() string { return e.Reason }

// Limits of the verification.
const (
	// DomainVerifyPerHour is how many times a manager may ask to check one domain in an hour.
	DomainVerifyPerHour = 6
	dnsTimeout          = 3 * time.Second
	maxTXTAnswers       = 10
	maxTXTLength        = 255
	// challengePrefix starts the value to publish; the rest is 128 random bits.
	challengePrefix = "backd-verify="
	// ChallengeLabel is the name under the domain that holds the record.
	ChallengeLabel = "_backd-challenge"
)

// pendingChecks are the waits between the automatic checks of a claim not proved yet; after the last
// the claim stays pending until the manager asks for a check.
var pendingChecks = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

// freeMail are the domains of the common free-mail providers: nobody proves them.
var freeMail = []string{
	"gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com", "msn.com", "yahoo.com", "ymail.com",
	"icloud.com", "me.com", "mac.com", "proton.me", "protonmail.com", "pm.me", "aol.com", "gmx.com", "gmx.net",
	"mail.com", "zoho.com", "yandex.com", "tutanota.com", "tuta.com", "hey.com", "fastmail.com",
}

// OrgDomain is an organization's claim on a domain.
type OrgDomain struct {
	ID     string // <org>:<domain>
	Org    string
	Domain string // lower case, in ASCII (punycode)
	State  string
	// Challenge is the value to publish; it is public once published, so it is kept as is.
	Challenge string
	// Discoverable lets the domain be used to find the organization from an address; LinkAccounts
	// lets an organization sign-in on the domain link existing accounts.
	Discoverable bool
	LinkAccounts bool
	// Failures counts the failed checks in a row; Checks the automatic checks of a pending claim.
	Failures                                          int
	Checks                                            int
	CreatedBy                                         string
	CreatedAt, VerifiedAt, LastCheckedAt, NextCheckAt time.Time
}

// DomainRecord is the DNS record to publish.
type DomainRecord struct {
	Type, Name, Value string
}

// DomainView is a claim as the API shows it.
type DomainView struct {
	OrgDomain
	Record DomainRecord
	// Reason says, after a check that did not prove the domain, why: record_not_found, mismatch or
	// resolver_error.
	Reason string
}

func newDomainView(d OrgDomain) DomainView {
	return DomainView{OrgDomain: d, Record: DomainRecord{Type: "TXT", Name: ChallengeLabel + "." + d.Domain, Value: d.Challenge}}
}

// DomainResolver reads TXT records. The default asks the host's resolver, or the realm's
// organizations.domains.resolvers.
type DomainResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// resolverFor is the resolver the realm's settings ask for.
func resolverFor(d *registry.OrgDomainsSettings) DomainResolver {
	r := &net.Resolver{}
	if d != nil && len(d.Resolvers) > 0 {
		servers := slices.Clone(d.Resolvers)
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var last error
			for _, srv := range servers {
				if _, _, err := net.SplitHostPort(srv); err != nil {
					srv = net.JoinHostPort(srv, "53")
				}
				c, err := (&net.Dialer{Timeout: dnsTimeout}).DialContext(ctx, network, srv)
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		}}
	}
	return r
}

func (s *Users) domainSettings() (*registry.OrgDomainsSettings, error) {
	o, err := s.orgs()
	if err != nil || o.Domains == nil {
		return nil, ErrDomainsDisabled
	}
	return o.Domains, nil
}

// NormalizeDomain writes a domain the way claims hold it: lower case, ASCII (punycode), no trailing
// dot, and a name that exists on the public internet (at least two labels, no address, not a public
// suffix). The empty string and an error say what is wrong.
func NormalizeDomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if d == "" || net.ParseIP(d) != nil {
		return "", &DomainRefusedError{Reason: fmt.Sprintf("%q isn't a domain name", raw)}
	}
	ascii, err := idna.Lookup.ToASCII(d)
	if err != nil {
		return "", &DomainRefusedError{Reason: fmt.Sprintf("%q isn't a valid domain name", raw)}
	}
	if msg := registry.DomainProblem(ascii); msg != "" {
		return "", &DomainRefusedError{Reason: msg}
	}
	if suffix, _ := publicsuffix.PublicSuffix(ascii); suffix == ascii {
		return "", &DomainRefusedError{Reason: fmt.Sprintf("%q is a public suffix: claim a domain under it", ascii)}
	}
	return ascii, nil
}

// refusedDomain says why nobody may claim d (already normalized), or "".
func (s *Users) refusedDomain(d string, set *registry.OrgDomainsSettings) string {
	if slices.Contains(freeMail, d) {
		return fmt.Sprintf("%q is a free-mail domain: nobody can prove it", d)
	}
	if slices.Contains(set.Blocked, d) {
		return fmt.Sprintf("%q is blocked by this realm", d)
	}
	return ""
}

func newChallenge() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return challengePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func domainID(org, domain string) string { return org + ":" + domain }

// domainAccess checks that the caller may manage the organization's domains: a member holding
// manage: domains. userID "" is a realm administrator, who needs the organization to exist.
func (s *Users) domainAccess(ctx context.Context, userID, orgID string) (Org, error) {
	if _, err := s.domainSettings(); err != nil {
		return Org{}, err
	}
	if userID == "" {
		org, err := s.Store.OrgByID(ctx, orgID)
		if err != nil {
			return Org{}, err
		}
		if org.Status == OrgDeleted {
			return Org{}, ErrOrgNotActive
		}
		return org, nil
	}
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return Org{}, err
	}
	if _, err := s.requireCaps(m, registry.CapDomains); err != nil {
		return Org{}, err
	}
	return m.Org, nil
}

// OrgDomainsAllowed says whether the caller may manage the organization's domains; the HTTP layer
// asks before it asks for a fresh proof of identity. userID "" is an administrator.
func (s *Users) OrgDomainsAllowed(ctx context.Context, userID, orgID string) error {
	_, err := s.domainAccess(ctx, userID, orgID)
	return err
}

func by(userID string) string {
	if userID == "" {
		return "admin"
	}
	return "user:" + userID
}

// ListOrgDomains returns the organization's claims, sorted by domain.
func (s *Users) ListOrgDomains(ctx context.Context, userID, orgID string) ([]DomainView, error) {
	if _, err := s.domainAccess(ctx, userID, orgID); err != nil {
		return nil, err
	}
	list, err := s.Store.OrgDomains(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]DomainView, len(list))
	for i, d := range list {
		out[i] = newDomainView(d)
	}
	return out, nil
}

// ClaimOrgDomain starts a claim: it issues the challenge to publish.
func (s *Users) ClaimOrgDomain(ctx context.Context, userID, orgID, domain string) (DomainView, error) {
	org, err := s.domainAccess(ctx, userID, orgID)
	if err != nil {
		return DomainView{}, err
	}
	set := s.Settings.Organizations.Domains
	d, err := NormalizeDomain(domain)
	if err != nil {
		return DomainView{}, err
	}
	if msg := s.refusedDomain(d, set); msg != "" {
		return DomainView{}, &DomainRefusedError{Reason: msg}
	}
	have, err := s.Store.OrgDomains(ctx, org.ID)
	if err != nil {
		return DomainView{}, err
	}
	for _, h := range have {
		if h.Domain == d {
			return DomainView{}, ErrDomainExists
		}
	}
	if len(have) >= set.MaxPerOrg {
		return DomainView{}, ErrDomainsLimit
	}
	challenge, err := newChallenge()
	if err != nil {
		return DomainView{}, err
	}
	now := s.now()
	claim := OrgDomain{
		ID: domainID(org.ID, d), Org: org.ID, Domain: d, State: DomainPending, Challenge: challenge, LinkAccounts: true,
		CreatedBy: by(userID), CreatedAt: now, NextCheckAt: now.Add(pendingChecks[0]),
	}
	if err := s.Store.PutOrgDomain(ctx, claim); err != nil {
		return DomainView{}, err
	}
	s.Audit(ctx, AuditOrgDomainAdded, orgTarget(org.ID), map[string]any{"domain": d, "by": by(userID)})
	return newDomainView(claim), nil
}

// lookup reads the TXT records of the challenge name, within the limits that keep a check bounded:
// three seconds, ten answers, 255 bytes each. The reason says why it found none.
func (s *Users) lookup(ctx context.Context, domain, challenge string) (found bool, reason string) {
	res := s.DNS
	if res == nil {
		res = resolverFor(s.Settings.Organizations.Domains)
	}
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	records, err := res.LookupTXT(ctx, ChallengeLabel+"."+domain)
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return false, "record_not_found"
	case err != nil:
		return false, "resolver_error"
	}
	if len(records) > maxTXTAnswers {
		records = records[:maxTXTAnswers]
	}
	for _, r := range records {
		if len(r) <= maxTXTLength && r == challenge {
			return true, ""
		}
	}
	if len(records) == 0 {
		return false, "record_not_found"
	}
	return false, "mismatch"
}

// VerifyOrgDomain checks the record now. A proved domain becomes verified unless another organization
// holds it (ErrDomainTaken); one that is not proved stays as it is and the view says why.
func (s *Users) VerifyOrgDomain(ctx context.Context, userID, orgID, domain string) (DomainView, error) {
	org, err := s.domainAccess(ctx, userID, orgID)
	if err != nil {
		return DomainView{}, err
	}
	d, err := s.Store.OrgDomain(ctx, org.ID, strings.ToLower(domain))
	if errors.Is(err, ErrNotFound) {
		return DomainView{}, ErrDomainNotFound
	}
	if err != nil {
		return DomainView{}, err
	}
	if err := s.RateLimit(ctx, "domain-verify:"+d.ID, DomainVerifyPerHour, time.Hour); err != nil {
		return DomainView{}, err
	}
	d, reason, err := s.checkDomain(ctx, d)
	if err != nil {
		return DomainView{}, err
	}
	v := newDomainView(d)
	v.Reason = reason
	return v, nil
}

// checkDomain runs one check of a claim and records the outcome: proved → verified; not proved → a
// failure counted for a verified domain (which lapses after lapse_after of them), a growing wait for a
// pending one.
func (s *Users) checkDomain(ctx context.Context, d OrgDomain) (OrgDomain, string, error) {
	set := s.Settings.Organizations.Domains
	now := s.now()
	found, reason := s.lookup(ctx, d.Domain, d.Challenge)
	d.LastCheckedAt = now
	was := d.State
	switch {
	case found:
		d.Failures, d.NextCheckAt = 0, now.Add(set.Recheck)
		if d.State != DomainVerified {
			d.State, d.VerifiedAt, d.Checks = DomainVerified, now, 0
		}
	case d.State == DomainPending:
		d.Checks++
		d.NextCheckAt = time.Time{}
		if d.Checks < len(pendingChecks) {
			d.NextCheckAt = now.Add(pendingChecks[d.Checks])
		}
	case d.State == DomainVerified:
		d.Failures++
		d.NextCheckAt = now.Add(set.Recheck)
		if d.Failures >= set.LapseAfter {
			d.State = DomainLapsed
		}
	default: // lapsed: look again at the usual pace
		d.NextCheckAt = now.Add(set.Recheck)
	}
	if err := s.Store.PutOrgDomain(ctx, d); err != nil {
		if errors.Is(err, ErrDomainTaken) {
			// Proved, but another organization holds it: this claim does not become verified.
			d.State, d.Failures = was, 0
			if was == DomainPending {
				d.NextCheckAt = time.Time{}
			}
			_ = s.Store.PutOrgDomain(ctx, d)
			return d, reason, ErrDomainTaken
		}
		return d, reason, err
	}
	switch {
	case d.State == DomainVerified && was != DomainVerified:
		s.Audit(ctx, AuditOrgDomainVerified, orgTarget(d.Org), map[string]any{"domain": d.Domain, "again": was == DomainLapsed})
	case d.State == DomainLapsed && was == DomainVerified:
		s.Audit(ctx, AuditOrgDomainLapsed, orgTarget(d.Org), map[string]any{"domain": d.Domain})
		s.noticeDomainLapsed(ctx, d)
	}
	return d, reason, nil
}

// noticeDomainLapsed tells the people who manage the organization's domains.
func (s *Users) noticeDomainLapsed(ctx context.Context, d OrgDomain) {
	if s.Settings.Email == nil {
		return
	}
	holders, err := s.orgHolders(ctx, d.Org, registry.CapDomains)
	if err != nil {
		return
	}
	for _, m := range holders {
		u, err := s.Store.UserByID(ctx, m.User)
		if err != nil || u.Disabled || !u.ErasedAt.IsZero() {
			continue
		}
		s.queueNotice(ctx, EmailRequest{Kind: email.SecurityNotice, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u), Org: d.Org, Notice: true, Event: "domain-lapsed"})
	}
}

// DomainUpdate changes what an organization lets a domain be used for; nil leaves a setting.
type DomainUpdate struct {
	Discoverable, LinkAccounts *bool
}

// UpdateOrgDomain sets the switches of a claim.
func (s *Users) UpdateOrgDomain(ctx context.Context, userID, orgID, domain string, in DomainUpdate) (DomainView, error) {
	org, err := s.domainAccess(ctx, userID, orgID)
	if err != nil {
		return DomainView{}, err
	}
	d, err := s.Store.OrgDomain(ctx, org.ID, strings.ToLower(domain))
	if errors.Is(err, ErrNotFound) {
		return DomainView{}, ErrDomainNotFound
	}
	if err != nil {
		return DomainView{}, err
	}
	if in.Discoverable != nil {
		d.Discoverable = *in.Discoverable
	}
	if in.LinkAccounts != nil {
		d.LinkAccounts = *in.LinkAccounts
	}
	if err := s.Store.PutOrgDomain(ctx, d); err != nil {
		return DomainView{}, err
	}
	s.Audit(ctx, AuditOrgDomainSettings, orgTarget(org.ID), map[string]any{"domain": d.Domain, "discoverable": d.Discoverable, "link_accounts": d.LinkAccounts, "by": by(userID)})
	return newDomainView(d), nil
}

// RemoveOrgDomain releases a claim (a verified one frees the domain for another organization).
func (s *Users) RemoveOrgDomain(ctx context.Context, userID, orgID, domain string) error {
	org, err := s.domainAccess(ctx, userID, orgID)
	if err != nil {
		return err
	}
	d := strings.ToLower(domain)
	if err := s.Store.DeleteOrgDomain(ctx, org.ID, d); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainNotFound
		}
		return err
	}
	s.Audit(ctx, AuditOrgDomainRemoved, orgTarget(org.ID), map[string]any{"domain": d, "by": by(userID)})
	return nil
}

// RemoveOrgDomains releases every claim of an organization: what the cleanup of a deleted one does.
// It can be repeated.
func (s *Users) RemoveOrgDomains(ctx context.Context, orgID string) (int, error) {
	return s.Store.DeleteOrgDomains(ctx, orgID)
}

// CheckOrgDomains runs the checks that are due: the re-check of verified and lapsed domains, and the
// growing-interval checks of claims not proved yet. It is bounded to limit claims a call (the worker
// calls it every minute) and returns how many it checked.
func (s *Users) CheckOrgDomains(ctx context.Context, limit int) (int, error) {
	if _, err := s.domainSettings(); err != nil {
		return 0, nil
	}
	due, err := s.Store.DueOrgDomains(ctx, s.now(), limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		if _, _, err := s.checkDomain(ctx, d); err != nil && !errors.Is(err, ErrDomainTaken) {
			if s.Log != nil {
				s.Log.Warn("check a domain", "org", d.Org, "domain", d.Domain, "error", err)
			}
			continue
		}
		n++
	}
	return n, nil
}

// VerifiedDomainOf returns the organization that holds the domain as verified ("" for none): what
// the features that rely on a proved domain ask.
func (s *Users) VerifiedDomainOf(ctx context.Context, domain string) (OrgDomain, bool) {
	d, err := s.Store.VerifiedOrgDomain(ctx, strings.ToLower(domain))
	return d, err == nil
}

// DiscoveredOrg is an organization a domain leads to: what an application needs to send the person to its
// sign-in.
type DiscoveredOrg struct {
	Org       string
	Name      string
	Providers []string // the names of the organization's usable connections
}

// Limits of discovery: an application asks when a sign-in page takes an address, so the per-address one
// is the one that matters against someone sweeping addresses.
const (
	discoverPerIPPerMinute      = 60
	discoverPerAddressPerHour   = 10
	discoverAddressWindow       = time.Hour
	discoverIPWindow            = time.Minute
	discoverUnknownOrgReadsOnly = "0" // an id no organization has: the reads of a miss look like the reads of a hit
)

// DiscoverOrgs answers "which organization is this address?" (design/sso.md §10.5): the one whose
// verified, discoverable domain it is on, if that organization has a connection people can use. Every
// other case (an unknown address, a domain nobody proved, one that does not let itself be found, an
// organization with no working connection) answers the same: nothing, and the same reads are made, so
// neither the answer nor its time says which. The person is never told whether an account exists.
func (s *Users) DiscoverOrgs(ctx context.Context, address, ip string) ([]DiscoveredOrg, error) {
	if _, err := s.domainSettings(); err != nil {
		return nil, err
	}
	if err := s.RateLimit(ctx, "discover-ip:"+ip, discoverPerIPPerMinute, discoverIPWindow); err != nil {
		return nil, err
	}
	addr, err := registry.NormalizeEmail(address)
	if err != nil {
		addr = ""
	}
	if err := s.RateLimit(ctx, "discover-address:"+addr, discoverPerAddressPerHour, discoverAddressWindow); err != nil {
		return nil, err
	}
	claim, found := s.VerifiedDomainOf(ctx, domainOf(addr))
	org, orgID := Org{}, discoverUnknownOrgReadsOnly
	if found && claim.Discoverable {
		orgID = claim.Org
	}
	org, orgErr := s.Store.OrgByID(ctx, orgID)
	conns, connErr := s.Store.OrgConnections(ctx, orgID)
	if !found || !claim.Discoverable || orgErr != nil || connErr != nil || org.Status != OrgActive {
		return []DiscoveredOrg{}, nil
	}
	sso := s.Settings.Organizations.SSO
	var providers []string
	for _, c := range conns {
		if !c.Enabled || !sso.Allows(c.Provider) {
			continue
		}
		if _, err := s.Store.SecretByScope(ctx, OrgScope(org.ID), SSOSecretName(c.Provider)); err == nil {
			providers = append(providers, c.Provider)
		}
	}
	if len(providers) == 0 {
		return []DiscoveredOrg{}, nil
	}
	return []DiscoveredOrg{{Org: org.ID, Name: org.Name, Providers: providers}}, nil
}
