package servercli

import (
	"context"
	"errors"
	"fmt"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/cli/admin"
	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/server"
	"github.com/fernandezvara/backd/internal/settings"
)

func secretRotateKey(c *kli.CommandContext) error {
	return rotateSecretsKey(context.Background(), c.Getenv, cmdutil.IOOf(c))
}

// rotateSecretsKey re-encrypts every auth-enabled realm's secrets from
// the old (currently configured) master key to a new one, in place.
func rotateSecretsKey(ctx context.Context, getenv func(string) string, uio cmdutil.IO) error {
	oldKey, err := settings.LoadKeyMaterial(getenv, "BACKD_SECRETS_KEY")
	if err != nil {
		return err
	}
	if oldKey == "" {
		return errors.New("BACKD_SECRETS_KEY (or BACKD_SECRETS_KEY_FILE) is not set: nothing to rotate from")
	}
	newKey, err := settings.LoadKeyMaterial(getenv, "BACKD_SECRETS_NEW_KEY")
	if err != nil {
		return err
	}
	if newKey == "" {
		return errors.New("BACKD_SECRETS_NEW_KEY (or BACKD_SECRETS_NEW_KEY_FILE) is not set: nothing to rotate to")
	}
	oldCipher, err := auth.NewSecretCipher([]byte(oldKey))
	if err != nil {
		return fmt.Errorf("BACKD_SECRETS_KEY: %w", err)
	}
	newCipher, err := auth.NewSecretCipher([]byte(newKey))
	if err != nil {
		return fmt.Errorf("BACKD_SECRETS_NEW_KEY: %w", err)
	}
	if oldCipher.KeyID == newCipher.KeyID {
		return errors.New("BACKD_SECRETS_KEY and BACKD_SECRETS_NEW_KEY are the same key")
	}
	a, err := server.Setup(ctx, getenv, uio.Stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	for _, realm := range a.Registry().SortedRealms() {
		if !realm.Settings.AuthEnabled {
			continue
		}
		n, err := rotateRealmSecrets(ctx, a, realm.Name, oldCipher, newCipher)
		if err != nil {
			return fmt.Errorf("%s: %w", realm.Name, err)
		}
		if n > 0 {
			fmt.Fprintf(uio.Stdout, "%s: %d secret(s) rotated\n", realm.Name, n)
		}
	}
	return nil
}

func rotateRealmSecrets(ctx context.Context, a *server.App, realm string, oldCipher, newCipher *auth.SecretCipher) (int, error) {
	store := mongodb.NewAuthStore(a.Client(), realm)
	secrets, err := store.ListSecrets(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range secrets {
		if s.KeyID == newCipher.KeyID {
			continue // already rotated (a retry after a partial failure)
		}
		plain, err := oldCipher.Open(s.Ciphertext, s.Nonce, s.KeyID)
		if err != nil {
			return n, fmt.Errorf("%s: %w", admin.SecretRef(s.Scope(), s.Name), err)
		}
		ct, nonce, err := newCipher.Seal(plain)
		if err != nil {
			return n, err
		}
		s.Ciphertext, s.Nonce = ct, nonce
		if s.PrevCiphertext != "" { // a key whose rotation is unfinished holds two values
			prev, err := oldCipher.Open(s.PrevCiphertext, s.PrevNonce, s.KeyID)
			if err != nil {
				return n, fmt.Errorf("%s: %w", admin.SecretRef(s.Scope(), s.Name), err)
			}
			if s.PrevCiphertext, s.PrevNonce, err = newCipher.Seal(prev); err != nil {
				return n, err
			}
		}
		s.KeyID = newCipher.KeyID
		if err := store.UpsertSecret(ctx, s); err != nil {
			return n, fmt.Errorf("%s: %w", admin.SecretRef(s.Scope(), s.Name), err)
		}
		n++
	}
	if n > 0 {
		u := &auth.Users{Store: store, Realm: realm, Log: a.Log()}
		u.Audit(ctx, auth.AuditSecretsRotated, "", map[string]any{"count": n})
	}
	return n, nil
}
