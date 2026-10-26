package auth

import "testing"

// A slug may not look like an organization id, so a name can never be taken for another
// organization's id when both are accepted in the same place.
func TestSlugsMayNotLookLikeIDs(t *testing.T) {
	for slug, ok := range map[string]bool{
		"acme-corp":             true,
		"0123456789abcdefghij":  false, // an id's shape
		"0123456789abcdefghi":   true,  // one short
		"0123456789abcdefghijk": true,  // one long
		"0123456789abcdefghiw":  true,  // w is not in an id's alphabet
		"":                      false, // a slug is required
	} {
		if err := validSlug(slug); (err == nil) != ok {
			t.Errorf("validSlug(%q) = %v, want ok=%v", slug, err, ok)
		}
	}
	if !LooksLikeOrgID("dbes1r9spovpar7kr700") || LooksLikeOrgID("acme-corp") {
		t.Error("LooksLikeOrgID")
	}
}
