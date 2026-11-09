package authcore

import (
	"errors"
	"fmt"
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
