package auth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// The realm's authentication data that moves with an app (design/export-import.md §7): the
// system collections an export package can hold, in the order an import writes them.
var PortableCollections = []string{"users", "identities", "orgs", "org_members", "org_teams", "org_connections", "mfa", "devices"}

// PortableSealed names, per collection, the fields sealed with the realm's secrets cipher. An
// export opens them and seals them again under the one-time export key; an import (after the
// CLI opened them with that key) seals the plaintext under the target realm's cipher.
var PortableSealed = map[string][]string{
	"identities": {"apple_refresh_token"},
	"mfa":        {"secret"},
	"devices":    {"token"},
}

// PortableReset names fields an export carries empty because their value can't travel: the
// recovery codes are keyed hashes under the realm's master key and can't be re-keyed (users
// get new ones), and a device is tied to a session that is not exported.
func PortableReset(collection string) map[string]any {
	switch collection {
	case "mfa":
		return map[string]any{"recovery_codes": []any{}}
	case "devices":
		return map[string]any{"session_id": ""}
	}
	return nil
}

// PlainKey wraps a sealed field's plaintext in a document an import receives.
const PlainKey = "$plain"

// Import outcomes of one portable document.
const (
	PortableCreated   = "created"
	PortableReplaced  = "replaced"
	PortableSkipped   = "skipped"
	PortableUnchanged = "unchanged"
	PortableConflict  = "conflict"
	PortableInvalid   = "invalid"
)

// PortableResult is what one document of an import came to.
type PortableResult struct {
	ID      string
	Outcome string
	Reason  string
}

// PortableStore is what exporting and importing authentication data needs of a store. The
// documents are raw (the stored shape with "id" for "_id", times as time.Time).
type PortableStore interface {
	// ExportSystem returns up to limit documents of a portable collection with an id after
	// afterID, ordered by id.
	ExportSystem(ctx context.Context, collection, afterID string, limit int) ([]map[string]any, error)
	// GetSystem returns one document of a portable collection, or false.
	GetSystem(ctx context.Context, collection, id string) (map[string]any, bool, error)
	// ImportSystem applies documents by mode ("create", "skip" or "replace"); with write false
	// it only says what would happen, unique indexes included. An identical document is
	// unchanged in every mode.
	ImportSystem(ctx context.Context, collection string, docs []map[string]any, mode string, write bool) ([]PortableResult, error)
}

// ErrNotPortable means the realm's store can't export authentication data.
var ErrNotPortable = errors.New("this store can't export or import authentication data")

func (s *Users) portable() (PortableStore, error) {
	p, ok := s.Store.(PortableStore)
	if !ok {
		return nil, ErrNotPortable
	}
	return p, nil
}

// AuthAnonymize turns the authentication data of an export into that of a staging copy:
// emails become user-<id>@example.invalid, password hashes are dropped (or replaced by one
// known password's), identities of external providers stay behind, organization sign-in
// connections keep their shape with the client ids blanked, and second factors and
// devices are not exported at all (see AnonymizedCollections).
type AuthAnonymize struct {
	// PasswordHash, when set, is the hash every password identity gets.
	PasswordHash string
}

// AnonymizedCollections are the portable collections an anonymized export carries.
func AnonymizedCollections() []string {
	return []string{"users", "identities", "orgs", "org_members", "org_teams", "org_connections"}
}

// AnonymousEmail is the address an anonymized export gives the user with that id.
func AnonymousEmail(userID string) string { return "user-" + userID + "@example.invalid" }

func (a *AuthAnonymize) apply(collection string, d map[string]any, now time.Time) (keep bool) {
	switch collection {
	case "users":
		id, _ := d["id"].(string)
		d["email"] = AnonymousEmail(id)
		delete(d, "pending_email")
		delete(d, "previous_email")
		delete(d, "admin_networks") // where a person works from is personal data too
		delete(d, "login_networks")
		// Whatever was done about passwords in production says nothing about the copy's own.
		delete(d, "password_expired_at")
		delete(d, "password_notified_at")
	case "org_connections":
		// A copy for staging keeps the shape of the connections, not the applications of
		// production: their client ids are blanked, so none of them can sign anyone in.
		d["client_id"] = ""
		d["native_client_ids"] = []any{}
	case "identities":
		if d["provider"] != ProviderPassword {
			return false
		}
		if a.PasswordHash != "" {
			// A new password, set now: a password epoch the staging realm holds doesn't expire it.
			d["password_hash"], d["password_changed_at"] = a.PasswordHash, now.UTC()
		} else {
			delete(d, "password_hash")
		}
	}
	return true
}

// ExportAuthDocs reads a page of a portable collection for an export package. Fields sealed
// with the realm's cipher are opened and sealed again under ek, the export key; a value that
// can't be opened (no cipher here, or sealed under another key) is left out and counted in
// unavailable. last is the id of the last document read and read how many there were, whatever
// was left out, so the next page can continue after it. Fields in PortableReset are emptied.
func (s *Users) ExportAuthDocs(ctx context.Context, collection, afterID string, limit int, ek *SecretCipher, anon *AuthAnonymize) (out []map[string]any, unavailable int, last string, read int, err error) {
	p, err := s.portable()
	if err != nil {
		return nil, 0, "", 0, err
	}
	docs, err := p.ExportSystem(ctx, collection, afterID, limit)
	if err != nil {
		return nil, 0, "", 0, err
	}
	read = len(docs)
	if read > 0 {
		last, _ = docs[read-1]["id"].(string)
	}
	out = make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		if anon != nil {
			if anon.apply(collection, d, s.now()) {
				out = append(out, d)
			}
			continue
		}
		maps.Copy(d, PortableReset(collection))
		carry := true
		for _, f := range PortableSealed[collection] {
			sealed, _ := d[f].(string)
			if sealed == "" {
				continue
			}
			plain, oerr := s.OpenToken(sealed)
			if oerr != nil {
				// Sealed under a key this instance doesn't hold: it can't be carried. A second
				// factor or a device without its secret is useless, so it stays behind; an
				// identity loses only its Apple token.
				unavailable++
				delete(d, f)
				carry = carry && collection == "identities"
				continue
			}
			again, serr := SealWith(ek, plain)
			if serr != nil {
				return nil, 0, "", 0, serr
			}
			d[f] = again
		}
		if carry {
			out = append(out, d)
		}
	}
	return out, unavailable, last, read, nil
}

// ImportAuthDocs validates (write false) or writes documents of a portable collection. Sealed
// fields arrive as {"$plain": value} and are sealed under this realm's cipher; a document
// holding one when this realm has no cipher is invalid.
func (s *Users) ImportAuthDocs(ctx context.Context, collection string, docs []map[string]any, mode string, write bool) ([]PortableResult, error) {
	p, err := s.portable()
	if err != nil {
		return nil, err
	}
	results := make([]PortableResult, len(docs))
	var ok []map[string]any
	var at []int
	for i, d := range docs {
		id, _ := d["id"].(string)
		results[i] = PortableResult{ID: id}
		if id == "" {
			results[i].Outcome, results[i].Reason = PortableInvalid, "id is missing"
			continue
		}
		bad := ""
		if collection == "org_connections" {
			bad = importedConnectionProblem(d)
		}
		for _, f := range PortableSealed[collection] {
			if bad != "" {
				break
			}
			v, has := d[f]
			if !has {
				continue
			}
			m, isMap := v.(map[string]any)
			plain, _ := m[PlainKey].(string)
			if !isMap || plain == "" {
				bad = f + " must arrive as {\"" + PlainKey + "\": value}"
				break
			}
			// A value already stored that opens to the same text stays as it is: sealing
			// again gives other bytes, and the document would stop being "identical".
			if have, found, gerr := p.GetSystem(ctx, collection, id); gerr != nil {
				return nil, gerr
			} else if found {
				if old, _ := have[f].(string); old != "" {
					if was, oerr := s.OpenToken(old); oerr == nil && was == plain {
						d[f] = old
						continue
					}
				}
			}
			sealed, serr := s.SealToken(plain)
			if serr != nil {
				bad = fmt.Sprintf("%s can't be sealed here: %v", f, serr)
				break
			}
			d[f] = sealed
		}
		if bad != "" {
			results[i].Outcome, results[i].Reason = PortableInvalid, bad
			continue
		}
		ok = append(ok, d)
		at = append(at, i)
	}
	if len(ok) == 0 {
		return results, nil
	}
	got, err := p.ImportSystem(ctx, collection, ok, mode, write)
	if err != nil {
		return nil, err
	}
	for j, r := range got {
		results[at[j]] = r
	}
	return results, nil
}

// PortableToPlain opens, with the export key, the fields of a package document that an
// export sealed under it, and hands them over as {"$plain": value} for an import to seal
// under the target realm's cipher. The key never leaves the machine that runs the import.
func PortableToPlain(collection string, doc map[string]any, ek *SecretCipher) error {
	for _, f := range PortableSealed[collection] {
		sealed, ok := doc[f].(string)
		if !ok || sealed == "" {
			continue
		}
		plain, err := OpenWith(ek, sealed)
		if err != nil {
			return fmt.Errorf("%s of %v: %w (is this the key the export gave?)", f, doc["id"], err)
		}
		doc[f] = map[string]any{PlainKey: plain}
	}
	return nil
}

// PortableWithoutSecrets prepares a package document for an import made without the export
// key: it reports whether the document can be imported at all (a second factor or a device is
// useless without its secret) and removes the sealed fields of the ones that can.
func PortableWithoutSecrets(collection string, doc map[string]any) (keep bool) {
	if collection == "mfa" || collection == "devices" {
		return false
	}
	for _, f := range PortableSealed[collection] {
		delete(doc, f)
	}
	return true
}

// ExportKey makes the cipher of an export key from its text (the key itself is 32 random
// bytes in base64url); its KeyID is the fingerprint the manifest records.
func ExportKey(text string) (*SecretCipher, error) {
	if len(text) < 43 {
		return nil, errors.New("this is not an export key")
	}
	return NewSecretCipher([]byte("backd-export|" + text))
}

// importedConnectionProblem says what is wrong with a connection of an import package ("" when
// it is fine): a package is as trusted as its administrator, but a connection that admits more
// than an API call could have set, or that the stores would read inconsistently, is refused. A
// blank client id is allowed: an anonymized export carries them blanked.
func importedConnectionProblem(d map[string]any) string {
	str := func(k string) string { v, _ := d[k].(string); return v }
	list := func(k string) []string {
		var out []string
		if arr, ok := d[k].([]any); ok {
			for _, e := range arr {
				s, _ := e.(string)
				out = append(out, s)
			}
		}
		return out
	}
	org, provider := str("org"), str("provider")
	switch {
	case org == "" || str("id") != org+":"+provider:
		return "id must be <org>:<provider>"
	case !slices.Contains(registry.SSOProviders, provider):
		return "provider must be one of " + strings.Join(registry.SSOProviders, ", ")
	}
	if id := str("client_id"); id != "" {
		if msg := registry.ClientIDProblem(provider, id); msg != "" {
			return "client_id " + msg
		}
	}
	switch provider {
	case registry.ProviderMicrosoft:
		if msg := registry.TenantProblem(str("tenant"), false); msg != "" {
			return "tenant " + msg
		}
		if str("hosted_domain") != "" {
			return "hosted_domain applies to Google only"
		}
	case registry.ProviderGoogle:
		if msg := registry.DomainProblem(str("hosted_domain")); msg != "" {
			return "hosted_domain " + msg
		}
		if str("tenant") != "" {
			return "tenant applies to Microsoft only"
		}
	}
	domains := list("email_domains")
	if len(domains) > MaxConnectionEmailDomains || len(list("native_client_ids")) > MaxConnectionNativeIDs || len(list("blocked")) > MaxConnectionBlocked {
		return "a list is longer than a connection may hold"
	}
	for _, dom := range domains {
		if msg := registry.DomainProblem(dom); msg != "" {
			return "email_domains " + msg
		}
	}
	return ""
}
