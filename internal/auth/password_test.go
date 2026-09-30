package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fastParams keep tests quick; production uses DefaultArgon2Params.
var fastParams = Argon2Params{Memory: 64, Time: 1, Threads: 1}

func TestCheckPassword(t *testing.T) {
	if _, err := CheckPassword("short", 12); err == nil || err.Error() != "password must be at least 12 characters" {
		t.Errorf("short: %v", err)
	}
	if _, err := CheckPassword(strings.Repeat("a", 129), 12); err == nil || !strings.Contains(err.Error(), "at most 128") {
		t.Errorf("long: %v", err)
	}
	// Lengths count characters, not bytes.
	if _, err := CheckPassword(strings.Repeat("ñ", 12), 12); err != nil {
		t.Errorf("12 two-byte characters rejected: %v", err)
	}
	// NFKC: the ligature "ﬁ" becomes "fi".
	if p, err := CheckPassword("ﬁ-correct-horse", 8); err != nil || p != "fi-correct-horse" {
		t.Errorf("normalized = %q, %v", p, err)
	}
	var pe *PolicyError
	if _, err := CheckPassword("x", 8); !errors.As(err, &pe) {
		t.Errorf("error %T is not a *PolicyError", err)
	}
}

func TestHashAndVerify(t *testing.T) {
	h := NewHasher(2, fastParams)
	ctx := context.Background()
	enc, err := h.Hash(ctx, "dev-p4ssw0rd!")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("encoded = %q", enc)
	}
	if again, _ := h.Hash(ctx, "dev-p4ssw0rd!"); again == enc {
		t.Error("two hashes of the same password are equal; salt not random")
	}
	for pw, want := range map[string]bool{"dev-p4ssw0rd!": true, "dev-p4ssw0rD!": false, "": false} {
		if ok, err := h.Verify(ctx, pw, enc); err != nil || ok != want {
			t.Errorf("Verify(%q) = %v, %v; want %v", pw, ok, err, want)
		}
	}

	// Parameters come from the hash, so a hasher with other settings still verifies it.
	other := NewHasher(1, Argon2Params{Memory: 128, Time: 2, Threads: 1})
	if ok, err := other.Verify(ctx, "dev-p4ssw0rd!", enc); !ok || err != nil {
		t.Errorf("verify with other params = %v, %v", ok, err)
	}

	// Verify normalizes like CheckPassword.
	norm, _ := CheckPassword("ﬁ-correct-horse", 8)
	enc2, _ := h.Hash(ctx, norm)
	if ok, _ := h.Verify(ctx, "ﬁ-correct-horse", enc2); !ok {
		t.Error("unnormalized input doesn't verify against its normalized hash")
	}

	for _, bad := range []string{"", "$argon2i$v=19$m=64,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=16$m=64,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=x$c2FsdA$aGFzaA", "$argon2id$v=19$m=64,t=1,p=1$!!$aGFzaA"} {
		if _, err := h.Verify(ctx, "pw", bad); err == nil {
			t.Errorf("Verify accepted malformed hash %q", bad)
		}
	}
}

func TestHasherQueues(t *testing.T) {
	h := NewHasher(1, fastParams)
	// Occupy the only slot.
	if err := h.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A caller whose deadline passes while queued gets ErrBusy.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "dev-p4ssw0rd!"); !errors.Is(err, ErrBusy) {
		t.Errorf("Hash while saturated = %v, want ErrBusy", err)
	}

	// A queued caller proceeds once the slot frees up.
	done := make(chan error, 1)
	go func() {
		_, err := h.Hash(context.Background(), "dev-p4ssw0rd!")
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	h.release()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("queued Hash: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued Hash never ran")
	}
}
