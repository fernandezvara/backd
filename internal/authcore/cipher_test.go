package authcore

import "testing"

const (
	testMasterKey  = "01234567890123456789012345678901"
	otherMasterKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestSecretCipherRoundTrip(t *testing.T) {
	c, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := c.Seal("sk_live_topsecret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Open(ct, nonce, c.KeyID)
	if err != nil || got != "sk_live_topsecret" {
		t.Fatalf("Open = %q, %v", got, err)
	}

	other, err := NewSecretCipher([]byte(otherMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(ct, nonce, c.KeyID); err == nil {
		t.Error("a value sealed under one key opened under another")
	}
	if _, err := c.Open(ct, nonce, other.KeyID); err == nil {
		t.Error("Open accepted a mismatched key id")
	}
	if _, err := c.Open(ct, "not-base64!!", c.KeyID); err == nil {
		t.Error("Open accepted an invalid nonce")
	}

	if _, err := NewSecretCipher([]byte("short")); err == nil {
		t.Error("NewSecretCipher accepted a key shorter than 32 characters")
	}
}

func TestSecretCipherDeterministicKeyID(t *testing.T) {
	a, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID != b.KeyID {
		t.Errorf("the same master key gave different key ids: %s vs %s", a.KeyID, b.KeyID)
	}
	c, err := NewSecretCipher([]byte(otherMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID == c.KeyID {
		t.Error("different master keys gave the same key id")
	}
}
