package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/settings"
)

// secret is a secret's metadata, as the admin API returns it.
type secret struct {
	Database  string `json:"database"`
	Org       string `json:"org"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
	// Fingerprint and UsedBy are set for keys.
	Fingerprint string   `json:"fingerprint"`
	UsedBy      []string `json:"used_by"`
}

func secretSet(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	name, database, org := str(c, "name"), str(c, "database"), str(c, "org")
	if org != "" && database != "" {
		return errors.New("--org and --database are different scopes: give one")
	}
	value, err := readSecretValue(ioOf(c), "Secret")
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
	if org != "" {
		body["org"] = org
	}
	if err := t.adm.Secrets.Put(t.ctx, name, body); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout(), "secret %q set\n", secretRef(database+orgScope(org), name))
	return nil
}

func secretList(c *kli.CommandContext) error {
	t, closeT, _, err := openTarget(c)
	if err != nil {
		return err
	}
	defer closeT()
	var page struct{ Items []secret }
	if err := t.adm.Secrets.List(t.ctx, &page); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCOPE\tNAME\tTYPE\tFINGERPRINT\tUPDATED\tUPDATED BY\tUSED BY")
	for _, s := range page.Items {
		scope := s.Database + orgScope(s.Org)
		if scope == "" {
			scope = "realm"
		}
		fp := "-"
		if s.Fingerprint != "" {
			fp = s.Fingerprint
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", scope, s.Name, s.Type, fp, s.UpdatedAt, s.UpdatedBy, strings.Join(s.UsedBy, ","))
	}
	return tw.Flush()
}

func secretDelete(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	name, database, org := str(c, "name"), str(c, "database"), str(c, "org")
	if org != "" && database != "" {
		return errors.New("--org and --database are different scopes: give one")
	}
	q := url.Values{}
	if database != "" {
		q.Set("database", database)
	}
	if org != "" {
		q.Set("org", org)
	}
	var ae *apiError
	var proof map[string]string
	if held, err := keyIsHeld(t, name); err != nil {
		return err
	} else if held && database == "" && org == "" {
		// Deleting a key loses what it protects: a person proves who they are again first.
		if proof, err = t.proof(ioOf(c)); err != nil {
			return err
		}
	}
	if err := t.adm.Secrets.Delete(t.ctx, name, q, withProof(proof)...); err != nil {
		if errors.As(err, &ae) && ae.Status == 404 {
			return fmt.Errorf("no secret named %q", secretRef(database+orgScope(org), name))
		}
		return err
	}
	fmt.Fprintf(c.Stdout(), "secret %q deleted\n", secretRef(database+orgScope(org), name))
	return nil
}

func secretRotateKey(c *kli.CommandContext) error {
	return rotateSecretsKey(context.Background(), c.Getenv, ioOf(c))
}

// secretRef names a secret as function.yaml would: realm.NAME for the realm scope, NAME for a
// database's, and org:<id>.NAME for an organization's (scope is the database or orgScope).
func secretRef(scope, name string) string {
	switch {
	case scope == "":
		return "realm." + name
	case strings.HasPrefix(scope, "org:"):
		return scope + "." + name
	}
	return name
}

// orgScope is the scope string of an organization, "" when org is empty.
func orgScope(org string) string {
	if org == "" {
		return ""
	}
	return auth.OrgScope(org)
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
			return n, fmt.Errorf("%s: %w", secretRef(s.Scope(), s.Name), err)
		}
		ct, nonce, err := newCipher.Seal(plain)
		if err != nil {
			return n, err
		}
		s.Ciphertext, s.Nonce = ct, nonce
		if s.PrevCiphertext != "" { // a key whose rotation is unfinished holds two values
			prev, err := oldCipher.Open(s.PrevCiphertext, s.PrevNonce, s.KeyID)
			if err != nil {
				return n, fmt.Errorf("%s: %w", secretRef(s.Scope(), s.Name), err)
			}
			if s.PrevCiphertext, s.PrevNonce, err = newCipher.Seal(prev); err != nil {
				return n, err
			}
		}
		s.KeyID = newCipher.KeyID
		if err := store.UpsertSecret(ctx, s); err != nil {
			return n, fmt.Errorf("%s: %w", secretRef(s.Scope(), s.Name), err)
		}
		n++
	}
	if n > 0 {
		u := &auth.Users{Store: store, Realm: realm, Log: a.log}
		u.Audit(ctx, auth.AuditSecretsRotated, "", map[string]any{"count": n})
	}
	return n, nil
}
