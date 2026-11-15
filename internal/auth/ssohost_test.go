package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/ssotest"
)

// backd's side of the sso ports passes the same suites as the library's in-memory fakes.

func ssoHost(clock func() time.Time) *auth.SSOHost {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	if clock == nil {
		clock = func() time.Time { return now }
	}
	u := newUsers(authtest.NewMemStore(), &now)
	u.Now = clock
	return &auth.SSOHost{Users: u, Realm: "r", BaseURL: "https://api.example"}
}

func TestSSOStoreConforms(t *testing.T) {
	ssotest.TestStore(t, func(now func() time.Time) sso.Store {
		st, _, _, _ := ssoHost(now).Ports()
		return st
	})
}

func TestSSODirectoryConforms(t *testing.T) {
	ssotest.TestDirectory(t, func(t *testing.T) ssotest.DirectoryHarness {
		h := ssoHost(nil)
		return ssotest.DirectoryHarness{
			Directory: dir(h),
			Seed: func(a sso.Account) {
				now := h.Users.Now()
				if err := h.Users.Store.CreateUser(context.Background(), auth.User{ID: a.ID, Email: a.Email, EmailVerified: a.EmailVerified, Roles: []string{}, CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			},
		}
	})
}

func TestSSOCatalogCallbackURLs(t *testing.T) {
	h := ssoHost(nil)
	_, c, _, _ := h.Ports()
	if u, ok := c.CallbackURL("realm", sso.Provider{Name: "google"}); !ok || u != "https://api.example/v1/r/_auth/oauth/google/callback" {
		t.Errorf("realm callback = %q, %v", u, ok)
	}
	if u, ok := c.CallbackURL("org:o1", sso.Provider{Name: "google", Org: "o1"}); !ok || u != "https://api.example/v1/r/_auth/orgs/o1/oauth/google/callback" {
		t.Errorf("org callback = %q, %v", u, ok)
	}
	h.BaseURL = ""
	if _, ok := c.CallbackURL("realm", sso.Provider{Name: "google"}); ok {
		t.Error("a callback without a public address")
	}
}

func TestSSOHostDataSurvivesTheStore(t *testing.T) {
	h := ssoHost(nil)
	st, _, _, _ := h.Ports()
	hd := auth.SSOHostData{ScopeOrg: "o1", Invitation: "hash", Locales: []string{"es"}, Consents: map[string]string{"terms": "v1"}}
	err := st.PutAttempt(context.Background(), "k", sso.Attempt{Provider: "google", Org: "o2", Intent: sso.IntentSignIn, Strategy: "web", Host: hd.Encode(), ExpiresAt: h.Users.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.TakeAttempt(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	got := auth.DecodeSSOHost(a.Host)
	if got.ScopeOrg != "o1" || got.Invitation != "hash" || got.Locales[0] != "es" || got.Consents["terms"] != "v1" || a.Scope != "org:o2" || a.Strategy != "web" {
		t.Errorf("attempt = %+v host = %+v", a, got)
	}
}

func dir(h *auth.SSOHost) sso.Directory {
	_, _, d, _ := h.Ports()
	return d
}
