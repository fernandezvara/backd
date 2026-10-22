package mongodb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/registry"
)

// usersOn builds the user service of a realm over its store, with a secrets cipher of its own.
func usersOn(t *testing.T, s *AuthStore, realm, master string, now time.Time) *auth.Users {
	t.Helper()
	c, err := auth.NewSecretCipher([]byte(master))
	if err != nil {
		t.Fatal(err)
	}
	return &auth.Users{
		Store: s, Realm: realm, Cipher: c, Cache: auth.NewSecretCache(),
		Hasher:   auth.NewHasher(1, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: registry.RealmSettings{PasswordMinLength: 12, IdleTimeout: time.Hour, MaxLifetime: 24 * time.Hour},
		Now:      func() time.Time { return now },
	}
}

// Users move to another realm, with another master key, and sign in there with their passwords
// and their authenticator app.
func TestAuthDataMovesBetweenRealms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2126, 10, 9, 12, 0, 0, 0, time.UTC)
	src, srcRealm := authFixture(t)
	dst, dstRealm := authFixture(t)
	from := usersOn(t, src, srcRealm, "source-master-key-0123456789abcdef-0", now)
	to := usersOn(t, dst, dstRealm, "target-master-key-0123456789abcdef-1", now)

	pw := "dev-p4ssw0rd!"
	ada, err := from.Create(ctx, "ada@example.com", &pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := from.Create(ctx, "bob@example.com", &pw); err != nil {
		t.Fatal(err)
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "acme", AccountName: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := from.SealToken(key.Secret())
	if err != nil {
		t.Fatal(err)
	}
	if err := src.PutMFA(ctx, auth.MFARecord{UserID: ada.ID, Secret: sealed, EnabledAt: now, RecoveryCodes: []string{from.Cipher.MAC("recovery", srcRealm, ada.ID, "AAAAABBBBB")}}); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.SealWith(from.Cipher, "fcm-token-1")
	if _, _, err := src.PutDevice(ctx, auth.Device{ID: "dev1", UserID: ada.ID, SessionID: "gone", Platform: "ios", TokenHash: auth.HashToken("fcm-token-1"), Token: token, CreatedAt: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}

	// Export: the page is read, its realm-sealed fields re-sealed under the export key, and the
	// documents go through the package's line format and back.
	ek, err := auth.ExportKey("a-one-time-export-key-of-43-characters-abcd")
	if err != nil {
		t.Fatal(err)
	}
	var packaged = map[string][][]byte{}
	for _, coll := range auth.PortableCollections {
		docs, unavailable, _, _, err := from.ExportAuthDocs(ctx, coll, "", 100, ek)
		if err != nil || unavailable != 0 {
			t.Fatalf("export %s: %v (%d unavailable)", coll, err, unavailable)
		}
		for _, d := range docs {
			line, err := exportpkg.EncodeDocument(d)
			if err != nil {
				t.Fatal(err)
			}
			packaged[coll] = append(packaged[coll], line)
		}
	}
	if len(packaged["users"]) != 2 || len(packaged["identities"]) != 2 || len(packaged["mfa"]) != 1 || len(packaged["devices"]) != 1 {
		t.Fatalf("exported: users %d identities %d mfa %d devices %d", len(packaged["users"]), len(packaged["identities"]), len(packaged["mfa"]), len(packaged["devices"]))
	}
	for _, line := range packaged["mfa"] {
		if s := string(line); strings.Contains(s, key.Secret()) || strings.Contains(s, from.Cipher.MAC("recovery", srcRealm, ada.ID, "AAAAABBBBB")) {
			t.Fatalf("the package holds the secret in clear or a recovery hash: %s", s)
		}
	}

	// Import: the CLI opens the sealed fields with the export key; the target seals them again.
	load := func(coll string, withKey bool) []map[string]any {
		var docs []map[string]any
		for _, line := range packaged[coll] {
			d, err := exportpkg.DecodeDocument(line)
			if err != nil {
				t.Fatal(err)
			}
			if withKey {
				if err := auth.PortableToPlain(coll, d, ek); err != nil {
					t.Fatal(err)
				}
			} else if !auth.PortableWithoutSecrets(coll, d) {
				continue
			}
			docs = append(docs, d)
		}
		return docs
	}
	for _, coll := range auth.PortableCollections {
		docs := load(coll, true)
		// Validation first: nothing is written.
		res, err := to.ImportAuthDocs(ctx, coll, docs, "create", false)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Outcome != auth.PortableCreated {
				t.Fatalf("validate %s: %+v", coll, r)
			}
		}
	}
	if _, err := dst.UserByEmail(ctx, "ada@example.com"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("validation wrote: %v", err)
	}
	for _, coll := range auth.PortableCollections {
		res, err := to.ImportAuthDocs(ctx, coll, load(coll, true), "create", true)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Outcome != auth.PortableCreated {
				t.Fatalf("write %s: %+v", coll, r)
			}
		}
		// Again: everything is identical, which is how an interrupted import finishes.
		res, _ = to.ImportAuthDocs(ctx, coll, load(coll, true), "create", true)
		for _, r := range res {
			if r.Outcome != auth.PortableUnchanged {
				t.Fatalf("again %s: %+v", coll, r)
			}
		}
	}

	// bob has no second factor: password only. ada is asked for her authenticator.
	if p, tok, err := to.Login(ctx, "bob@example.com", pw, ""); err != nil || tok == "" || p.Session.MFA {
		t.Errorf("bob: %v", err)
	}
	out, err := to.SignIn(ctx, "ada@example.com", pw, "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != auth.ChallengeMFA {
		t.Fatalf("ada's first factor: %+v %v", out, err)
	}
	code, err := totp.GenerateCodeCustom(key.Secret(), now, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.VerifyMFA(ctx, out.Challenge.Token, auth.MethodTOTP, code, ""); err != nil {
		t.Errorf("ada's authenticator code on the target: %v", err)
	}
	// The device's token is sealed under the target's key now.
	devs, err := dst.ListDevices(ctx, ada.ID)
	if err != nil || len(devs) != 1 {
		t.Fatalf("devices: %v %v", devs, err)
	}
	if plain, err := to.OpenToken(devs[0].Token); err != nil || plain != "fcm-token-1" || devs[0].SessionID != "" {
		t.Errorf("device: %q %v session %q", plain, err, devs[0].SessionID)
	}

	// Without the export key, users arrive without second factors and devices.
	other, otherRealm := authFixture(t)
	bare := usersOn(t, other, otherRealm, "third-master-key-0123456789abcdef-2", now)
	for _, coll := range auth.PortableCollections {
		if _, err := bare.ImportAuthDocs(ctx, coll, load(coll, false), "create", true); err != nil {
			t.Fatal(err)
		}
	}
	if p, tok, err := bare.Login(ctx, "ada@example.com", pw, ""); err != nil || tok == "" || p.Session.MFA {
		t.Errorf("ada without her second factor: %v", err)
	}

	// A different document with an id the target has is a conflict in create mode, and a
	// repeated email is a conflict even under another id.
	d := load("users", true)[0]
	d["roles"] = []any{"changed"}
	res, _ := to.ImportAuthDocs(ctx, "users", []map[string]any{d}, "create", true)
	if res[0].Outcome != auth.PortableConflict {
		t.Errorf("a different user: %+v", res[0])
	}
	if res, _ = to.ImportAuthDocs(ctx, "users", []map[string]any{d}, "skip", true); res[0].Outcome != auth.PortableSkipped {
		t.Errorf("skip: %+v", res[0])
	}
	if res, _ = to.ImportAuthDocs(ctx, "users", []map[string]any{d}, "replace", true); res[0].Outcome != auth.PortableReplaced {
		t.Errorf("replace: %+v", res[0])
	}
	twin := load("users", true)[0]
	twin["id"] = "someone-else"
	if res, _ = to.ImportAuthDocs(ctx, "users", []map[string]any{twin}, "create", false); res[0].Outcome != auth.PortableConflict {
		t.Errorf("a repeated email: %+v", res[0])
	}
	// A sealed field must arrive as plaintext for the target to seal.
	raw, _ := exportpkg.DecodeDocument(packaged["mfa"][0])
	if res, _ = to.ImportAuthDocs(ctx, "mfa", []map[string]any{raw}, "create", false); res[0].Outcome != auth.PortableInvalid {
		t.Errorf("a sealed secret: %+v", res[0])
	}
}
