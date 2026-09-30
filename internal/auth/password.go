package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// MaxPasswordLength is the longest accepted password, in characters.
const MaxPasswordLength = 128

// PolicyError is a password that doesn't meet the realm's policy.
type PolicyError struct{ Reason string }

func (e *PolicyError) Error() string { return "password " + e.Reason }

// CheckPassword applies the length policy and returns the password in
// Unicode NFKC form, which is what gets hashed. Lengths count characters.
func CheckPassword(password string, minLength int) (string, error) {
	p := norm.NFKC.String(password)
	n := utf8.RuneCountInString(p)
	switch {
	case n < minLength:
		return "", &PolicyError{fmt.Sprintf("must be at least %d characters", minLength)}
	case n > MaxPasswordLength:
		return "", &PolicyError{fmt.Sprintf("must be at most %d characters", MaxPasswordLength)}
	}
	return p, nil
}

// Argon2Params are argon2id cost parameters.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultArgon2Params follow RFC 9106's second recommendation (64 MiB,
// 3 passes), with one thread per hash: concurrency is capped per instance
// instead.
var DefaultArgon2Params = Argon2Params{Memory: 64 * 1024, Time: 3, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
)

// Hasher hashes and verifies passwords with argon2id. At most a fixed
// number of hashes run at once; callers queue for a slot until their
// context ends, which bounds memory under a flood of requests.
type Hasher struct {
	Params Argon2Params
	slots  chan struct{}
}

// NewHasher returns a Hasher running at most concurrency hashes at once
// (DefaultHashConcurrency when concurrency < 1).
func NewHasher(concurrency int, params Argon2Params) *Hasher {
	if concurrency < 1 {
		concurrency = DefaultHashConcurrency(params)
	}
	return &Hasher{Params: params, slots: make(chan struct{}, concurrency)}
}

// Concurrency is how many hashes may run at once.
func (h *Hasher) Concurrency() int { return cap(h.slots) }

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrBusy
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash returns the PHC-format argon2id hash of an already checked password.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	p := h.Params
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded. The password is
// normalized like CheckPassword does. Parameters are read from encoded,
// so hashes made with older parameters keep working.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(norm.NFKC.String(password)), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errors.New("unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return p, nil, nil, fmt.Errorf("invalid argon2 parameters: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, fmt.Errorf("invalid salt: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return p, nil, nil, errors.New("invalid hash")
	}
	return p, salt, key, nil
}
