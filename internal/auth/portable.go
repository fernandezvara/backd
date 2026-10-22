package auth

import (
	"context"
	"errors"
	"fmt"
)

// The realm's authentication data that moves with an app (design/export-import.md §7): the
// system collections an export package can hold, in the order an import writes them.
var PortableCollections = []string{"users", "identities", "orgs", "org_members", "org_teams", "mfa", "devices"}

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
// known password's), identities of external providers stay behind, and second factors and
// devices are not exported at all (see AnonymizedCollections).
type AuthAnonymize struct {
	// PasswordHash, when set, is the hash every password identity gets.
	PasswordHash string
}

// AnonymizedCollections are the portable collections an anonymized export carries.
func AnonymizedCollections() []string {
	return []string{"users", "identities", "orgs", "org_members", "org_teams"}
}

// AnonymousEmail is the address an anonymized export gives the user with that id.
func AnonymousEmail(userID string) string { return "user-" + userID + "@example.invalid" }

func (a *AuthAnonymize) apply(collection string, d map[string]any) (keep bool) {
	switch collection {
	case "users":
		id, _ := d["id"].(string)
		d["email"] = AnonymousEmail(id)
		delete(d, "pending_email")
		delete(d, "previous_email")
	case "identities":
		if d["provider"] != ProviderPassword {
			return false
		}
		if a.PasswordHash != "" {
			d["password_hash"] = a.PasswordHash
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
			if anon.apply(collection, d) {
				out = append(out, d)
			}
			continue
		}
		for f, v := range PortableReset(collection) {
			d[f] = v
		}
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
		for _, f := range PortableSealed[collection] {
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
