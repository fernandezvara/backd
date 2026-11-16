package auth_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// txtTable is a DomainResolver over a table; a name with no entry is not found.
type txtTable struct {
	mu      sync.Mutex
	records map[string][]string
	err     error
}

func (t *txtTable) set(name string, values ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.records == nil {
		t.records = map[string][]string{}
	}
	t.records[name] = values
}

func (t *txtTable) drop(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.records, name)
}

func (t *txtTable) LookupTXT(_ context.Context, name string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return nil, t.err
	}
	if v, ok := t.records[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

type domainFixture struct {
	svc   *Users
	store *authtest.MemStore
	dns   *txtTable
	clock *time.Time
}

// domainUsers is a realm with organizations holding two organizations, org1 and org2, and users u1
// (owner of both, with manage: domains), u2 (a member of org1 without it) and u3 (of neither).
func domainUsers(t *testing.T, domainsYAML string) *domainFixture {
	t.Helper()
	root := t.TempDir()
	realm := "auth:\n  strategies:\n    password: [password]\n  signup: open\norganizations:\n  domains: " + domainsYAML + "\n  roles:\n    owner: { manage: [org, members, roles, teams, domains] }\n    member: {}\n  default_roles: [member]\n"
	files := map[string]string{
		"acme/realm.yaml": realm, "acme/app/notes/schema.json": `{"type":"object"}`,
	}
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	store := authtest.NewMemStore()
	clock := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	dns := &txtTable{}
	svc := &Users{Store: store, Realm: "acme", Settings: reg.Realms["acme"].Settings, Now: func() time.Time { return clock }, DNS: dns}
	ctx := context.Background()
	for _, o := range []string{"org1", "org2"} {
		if err := store.CreateOrg(ctx, Org{ID: o, Name: o, Status: OrgActive}, OrgMember{Org: o, User: "u1", Roles: []RoleGrant{{Role: "owner"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutOrgMember(ctx, OrgMember{Org: "org1", User: "u2", Roles: []RoleGrant{{Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	return &domainFixture{svc: svc, store: store, dns: dns, clock: &clock}
}

func (f *domainFixture) prove(t *testing.T, org, domain string) DomainView {
	t.Helper()
	ctx := context.Background()
	v, err := f.svc.ClaimOrgDomain(ctx, "u1", org, domain)
	if err != nil {
		t.Fatalf("claim %s: %v", domain, err)
	}
	f.dns.set(v.Record.Name, v.Record.Value)
	if v, err = f.svc.VerifyOrgDomain(ctx, "u1", org, v.Domain); err != nil || v.State != DomainVerified {
		t.Fatalf("verify %s: %+v, %v", domain, v, err)
	}
	return v
}

func TestDomainsAreNormalizedAndSomeAreRefused(t *testing.T) {
	f := domainUsers(t, "{ blocked: [blocked.example] }")
	ctx := context.Background()
	for _, tc := range []struct{ in, want string }{
		{"Acme.Example", "acme.example"}, {" acme.example. ", "acme.example"}, {"bücher.example", "xn--bcher-kva.example"},
	} {
		v, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", tc.in)
		if err != nil || v.Domain != tc.want {
			t.Errorf("%q → %+v, %v; want %q", tc.in, v, err, tc.want)
			continue
		}
		if err := f.svc.RemoveOrgDomain(ctx, "u1", "org1", v.Domain); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"", "localhost", "10.0.0.1", "co.uk", "com", "gmail.com", "Outlook.com", "blocked.example", "not a domain", "-x.example", "a..example", strings.Repeat("a", 64) + ".example"} {
		var refused *DomainRefusedError
		if _, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", bad); !errors.As(err, &refused) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestAClaimShowsTheRecordToPublishAndIsLimited(t *testing.T) {
	f := domainUsers(t, "{ max_per_org: 2 }")
	ctx := context.Background()
	v, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "acme.example")
	if err != nil || v.State != DomainPending || v.Record.Type != "TXT" || v.Record.Name != "_backd-challenge.acme.example" || !strings.HasPrefix(v.Record.Value, "backd-verify=") || len(v.Record.Value) != len("backd-verify=")+22 {
		t.Fatalf("claim = %+v, %v", v, err)
	}
	if !v.LinkAccounts || v.Discoverable {
		t.Errorf("defaults: %+v", v)
	}
	w, _ := f.svc.ClaimOrgDomain(ctx, "u1", "org2", "acme.example") // another organization may claim it too
	if w.Record.Value == v.Record.Value {
		t.Error("two claims share a challenge")
	}
	if _, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "acme.example"); !errors.Is(err, ErrDomainExists) {
		t.Errorf("twice: %v", err)
	}
	if _, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "b.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "c.example"); !errors.Is(err, ErrDomainsLimit) {
		t.Errorf("over the limit: %v", err)
	}
}

func TestOnlyManagersOfDomainsMayClaim(t *testing.T) {
	f := domainUsers(t, "{ enabled: true }")
	ctx := context.Background()
	if _, err := f.svc.ClaimOrgDomain(ctx, "u2", "org1", "acme.example"); err == nil {
		t.Error("a member without manage: domains claimed")
	}
	if _, err := f.svc.ListOrgDomains(ctx, "u2", "org1"); err == nil {
		t.Error("a member without manage: domains listed")
	}
	if _, err := f.svc.ClaimOrgDomain(ctx, "u3", "org1", "acme.example"); !errors.Is(err, ErrNotOrgMember) {
		t.Errorf("a stranger: %v", err)
	}
	if _, err := f.svc.ClaimOrgDomain(ctx, "", "org1", "acme.example"); err != nil {
		t.Errorf("an administrator: %v", err)
	}
	if _, err := f.svc.ClaimOrgDomain(ctx, "", "nope", "x.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an organization that isn't there: %v", err)
	}
	// The realm switch: with it off nothing is there.
	f.svc.Settings.Organizations.Domains = nil
	if _, err := f.svc.ListOrgDomains(ctx, "u1", "org1"); !errors.Is(err, ErrDomainsDisabled) {
		t.Errorf("switched off: %v", err)
	}
}

func TestVerifyTellsWhyADomainIsNotProved(t *testing.T) {
	f := domainUsers(t, "{ enabled: true }")
	ctx := context.Background()
	v, _ := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "acme.example")
	check := func(want string) {
		t.Helper()
		got, err := f.svc.VerifyOrgDomain(ctx, "u1", "org1", "acme.example")
		if err != nil || got.State != DomainPending || got.Reason != want {
			t.Errorf("reason = %q (%v, %v), want %q", got.Reason, got.State, err, want)
		}
	}
	check("record_not_found")
	f.dns.set(v.Record.Name, "backd-verify=somebody-elses", "v=spf1 -all")
	check("mismatch")
	f.dns.set(v.Record.Name, strings.Repeat("x", 300))
	check("mismatch") // a value over 255 bytes is never taken
	f.dns.err = errors.New("servfail")
	check("resolver_error")
	f.dns.err = nil
	// More than ten answers: only the first ten are read, so the right one at the end is not seen.
	many := make([]string, 11)
	for i := range many {
		many[i] = "noise"
	}
	many[10] = v.Record.Value
	f.dns.set(v.Record.Name, many...)
	check("mismatch")
	f.dns.set(v.Record.Name, "noise", v.Record.Value)
	got, err := f.svc.VerifyOrgDomain(ctx, "u1", "org1", "acme.example")
	if err != nil || got.State != DomainVerified || got.VerifiedAt.IsZero() || got.Reason != "" {
		t.Errorf("proved: %+v, %v", got, err)
	}
}

func TestOnlyOneOrganizationHoldsAVerifiedDomain(t *testing.T) {
	f := domainUsers(t, "{ enabled: true }")
	ctx := context.Background()
	// org2 is pending for it: that blocks nobody.
	squat, _ := f.svc.ClaimOrgDomain(ctx, "u1", "org2", "acme.example")
	f.prove(t, "org1", "acme.example")
	// org2 shows the record (it controls the DNS too, as a copy of the other's value would not):
	// the domain is held, and the claim stays pending.
	f.dns.set(squat.Record.Name, squat.Record.Value)
	if _, err := f.svc.VerifyOrgDomain(ctx, "u1", "org2", "acme.example"); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("a second verification: %v", err)
	}
	if d, _ := f.store.OrgDomain(ctx, "org2", "acme.example"); d.State != DomainPending {
		t.Errorf("the loser is %s", d.State)
	}
	// The holder releases it: the other can have it.
	if err := f.svc.RemoveOrgDomain(ctx, "u1", "org1", "acme.example"); err != nil {
		t.Fatal(err)
	}
	if v, err := f.svc.VerifyOrgDomain(ctx, "u1", "org2", "acme.example"); err != nil || v.State != DomainVerified {
		t.Errorf("after the release: %+v, %v", v, err)
	}
	if got, ok := f.svc.VerifiedDomainOf(ctx, "acme.example"); !ok || got.Org != "org2" {
		t.Errorf("holder = %+v, %v", got, ok)
	}
}

func TestADomainLapsesAfterFailedChecksAndComesBack(t *testing.T) {
	f := domainUsers(t, "{ recheck: 24h, lapse_after: 3 }")
	ctx := context.Background()
	v := f.prove(t, "org1", "acme.example")
	step := func() { *f.clock = f.clock.Add(25 * time.Hour); _, _ = f.svc.CheckOrgDomains(ctx, 10) }
	state := func() OrgDomain { d, _ := f.store.OrgDomain(ctx, "org1", "acme.example"); return d }
	// The record is checked again and passes.
	step()
	if d := state(); d.State != DomainVerified || d.Failures != 0 {
		t.Fatalf("after a good re-check: %+v", d)
	}
	// It goes away: two failures are tolerated, the third lapses it.
	f.dns.drop(v.Record.Name)
	step()
	step()
	if d := state(); d.State != DomainVerified || d.Failures != 2 {
		t.Fatalf("after two failures: %+v", d)
	}
	step()
	if d := state(); d.State != DomainLapsed {
		t.Fatalf("after three: %+v", d)
	}
	// A lapsed domain holds nothing: another organization proves it.
	f.dns.set(v.Record.Name, v.Record.Value)
	other := f.prove(t, "org2", "acme.example")
	_ = other
	f.dns.set(v.Record.Name, v.Record.Value, other.Record.Value)
	// The first can't come back while the other holds it.
	step()
	if d := state(); d.State != DomainLapsed {
		t.Errorf("a lapsed domain returned over a holder: %+v", d)
	}
	if err := f.svc.RemoveOrgDomain(ctx, "u1", "org2", "acme.example"); err != nil {
		t.Fatal(err)
	}
	step()
	if d := state(); d.State != DomainVerified || d.Failures != 0 {
		t.Errorf("after the release: %+v", d)
	}
}

func TestAPendingClaimIsCheckedAtGrowingIntervalsThenLeftAlone(t *testing.T) {
	f := domainUsers(t, "{ enabled: true }")
	ctx := context.Background()
	v, _ := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "acme.example")
	// Nothing is due at once; a minute later the first check runs.
	if n, _ := f.svc.CheckOrgDomains(ctx, 10); n != 0 {
		t.Errorf("checked %d at the start", n)
	}
	*f.clock = f.clock.Add(61 * time.Second)
	if n, _ := f.svc.CheckOrgDomains(ctx, 10); n != 1 {
		t.Errorf("first check: %d", n)
	}
	// The record appears: the next scheduled check proves it.
	f.dns.set(v.Record.Name, v.Record.Value)
	*f.clock = f.clock.Add(6 * time.Minute)
	if n, _ := f.svc.CheckOrgDomains(ctx, 10); n != 1 {
		t.Errorf("second check: %d", n)
	}
	if d, _ := f.store.OrgDomain(ctx, "org1", "acme.example"); d.State != DomainVerified || d.NextCheckAt.IsZero() {
		t.Errorf("proved by the job: %+v", d)
	}
	// A claim that never shows the record is given up on, until someone asks for a check.
	w, _ := f.svc.ClaimOrgDomain(ctx, "u1", "org2", "never.example")
	_ = w
	for range 10 {
		*f.clock = f.clock.Add(25 * time.Hour)
		_, _ = f.svc.CheckOrgDomains(ctx, 10)
	}
	if d, _ := f.store.OrgDomain(ctx, "org2", "never.example"); d.State != DomainPending || !d.NextCheckAt.IsZero() {
		t.Errorf("a claim left alone: %+v", d)
	}
}

func TestVerifyIsLimitedPerDomain(t *testing.T) {
	f := domainUsers(t, "{ enabled: true }")
	ctx := context.Background()
	if _, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "acme.example"); err != nil {
		t.Fatal(err)
	}
	for range DomainVerifyPerHour {
		if _, err := f.svc.VerifyOrgDomain(ctx, "u1", "org1", "acme.example"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.VerifyOrgDomain(ctx, "u1", "org1", "acme.example"); err == nil {
		t.Error("a seventh check in an hour")
	}
}

func TestSettingsAndReleaseOfADomain(t *testing.T) {
	f := domainUsers(t, "{ enabled: true }")
	ctx := context.Background()
	if _, err := f.svc.ClaimOrgDomain(ctx, "u1", "org1", "acme.example"); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	v, err := f.svc.UpdateOrgDomain(ctx, "u1", "org1", "acme.example", DomainUpdate{Discoverable: &yes, LinkAccounts: &no})
	if err != nil || !v.Discoverable || v.LinkAccounts {
		t.Fatalf("update = %+v, %v", v, err)
	}
	if _, err := f.svc.UpdateOrgDomain(ctx, "u1", "org1", "nope.example", DomainUpdate{}); !errors.Is(err, ErrDomainNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if err := f.svc.RemoveOrgDomain(ctx, "u1", "org1", "nope.example"); !errors.Is(err, ErrDomainNotFound) {
		t.Errorf("remove unknown: %v", err)
	}
	// An organization deleted takes its claims, and the verified ones free the domain.
	f.prove(t, "org2", "other.example")
	n, err := f.svc.RemoveOrgDomains(ctx, "org2")
	if err != nil || n != 1 {
		t.Errorf("RemoveOrgDomains = %d, %v", n, err)
	}
	if _, ok := f.svc.VerifiedDomainOf(ctx, "other.example"); ok {
		t.Error("a deleted organization still holds a domain")
	}
	if n, _ := f.svc.RemoveOrgDomains(ctx, "org2"); n != 0 {
		t.Errorf("a second run found %d", n)
	}
}

func TestClaimsTravelAsPendingInAnExport(t *testing.T) {
	f := domainUsers(t, "{ enabled: true }")
	ctx := context.Background()
	f.prove(t, "org1", "acme.example")
	d, _ := f.store.OrgDomain(ctx, "org1", "acme.example")
	f.store.SeedPortable("org_domains", map[string]any{
		"id": d.ID, "org": d.Org, "domain": d.Domain, "state": "verified", "challenge": d.Challenge, "failures": 2,
		"verified_at": d.VerifiedAt, "next_check_at": d.NextCheckAt, "created_at": d.CreatedAt,
	})
	docs, _, _, _, err := f.svc.ExportAuthDocs(ctx, "org_domains", "", 10, nil, nil)
	if err != nil || len(docs) != 1 {
		t.Fatalf("export = %v, %v", docs, err)
	}
	got := docs[0]
	if got["state"] != "pending" || got["failures"] != 0 || got["verified_at"] != nil || got["next_check_at"] != nil || got["challenge"] != d.Challenge {
		t.Errorf("exported = %v", got)
	}
}
