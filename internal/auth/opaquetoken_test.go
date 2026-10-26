package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewOpaqueToken(t *testing.T) {
	a, err := newOpaqueToken("tst_")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newOpaqueToken("tst_")
	if a == b {
		t.Fatal("two tokens are equal")
	}
	body, ok := strings.CutPrefix(a, "tst_")
	if !ok {
		t.Fatalf("no prefix: %q", a)
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(raw) != 32 {
		t.Fatalf("body is %d bytes (%v), want 32 URL-safe bytes", len(raw), err)
	}
	plain, _ := newOpaqueToken("")
	if len(plain) != 43 {
		t.Fatalf("no-prefix token has %d characters, want 43", len(plain))
	}
}
