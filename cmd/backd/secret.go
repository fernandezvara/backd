package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"text/tabwriter"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/settings"
)

// secret is a secret's metadata, as the admin API returns it.
type secret struct {
	Database  string `json:"database"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

func secretSet(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	name, database := str(c, "name"), str(c, "database")
	value, err := readSecret(ioOf(c), false)
	if err != nil {
		return err
	}
	if value == "" {
		return errors.New("the value must not be empty")
	}
	body := map[string]any{"value": value}
	if database != "" {
		body["database"] = database
	}
	if err := t.call("PUT", "_admin/secrets/"+url.PathEscape(name), nil, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout(), "secret %q set\n", secretRef(database, name))
	return nil
}

func secretList(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var page struct{ Items []secret }
	if err := t.call("GET", "_admin/secrets", nil, nil, &page); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCOPE\tNAME\tUPDATED\tUPDATED BY")
	for _, s := range page.Items {
		scope := s.Database
		if scope == "" {
			scope = "realm"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", scope, s.Name, s.UpdatedAt, s.UpdatedBy)
	}
	return tw.Flush()
}

func secretDelete(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	name, database := str(c, "name"), str(c, "database")
	q := url.Values{}
	if database != "" {
		q.Set("database", database)
	}
	var ae *apiError
	if err := t.call("DELETE", "_admin/secrets/"+url.PathEscape(name), q, nil, nil); err != nil {
		if errors.As(err, &ae) && ae.Status == 404 {
			return fmt.Errorf("no secret named %q", secretRef(database, name))
		}
		return err
	}
	fmt.Fprintf(c.Stdout(), "secret %q deleted\n", secretRef(database, name))
	return nil
}

func secretRotateKey(c *kli.CommandContext) error {
	return rotateSecretsKey(context.Background(), c.Getenv, ioOf(c))
}

func secretRef(database, name string) string {
	if database == "" {
		return "realm." + name
	}
	return name
}

// rotateSecretsKey re-encrypts every auth-enabled realm's secrets from
// the old (currently configured) master key to a new one, in place.
func rotateSecretsKey(ctx context.Context, getenv func(string) string, uio userIO) error {
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
	a, err := setup(ctx, getenv, uio.stderr)
	if err != nil {
		return err
	}
	defer a.close()
	for _, realm := range a.reg.SortedRealms() {
		if !realm.Settings.AuthEnabled {
			continue
		}
		n, err := rotateRealmSecrets(ctx, a, realm.Name, oldCipher, newCipher)
		if err != nil {
			return fmt.Errorf("%s: %w", realm.Name, err)
		}
		if n > 0 {
			fmt.Fprintf(uio.stdout, "%s: %d secret(s) rotated\n", realm.Name, n)
		}
	}
	return nil
}

func rotateRealmSecrets(ctx context.Context, a *app, realm string, oldCipher, newCipher *auth.SecretCipher) (int, error) {
	store := mongodb.NewAuthStore(a.client, realm)
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
			return n, fmt.Errorf("%s: %w", secretRef(s.Database, s.Name), err)
		}
		ct, nonce, err := newCipher.Seal(plain)
		if err != nil {
			return n, err
		}
		s.Ciphertext, s.Nonce, s.KeyID = ct, nonce, newCipher.KeyID
		if err := store.UpsertSecret(ctx, s); err != nil {
			return n, fmt.Errorf("%s: %w", secretRef(s.Database, s.Name), err)
		}
		n++
	}
	if n > 0 {
		u := &auth.Users{Store: store, Realm: realm, Log: a.log}
		u.Audit(ctx, auth.AuditSecretsRotated, "", map[string]any{"count": n})
	}
	return n, nil
}
