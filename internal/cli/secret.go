package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/authcore"
	"github.com/fernandezvara/backd/internal/cmdutil"
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
	name, database, org := cmdutil.Str(c, "name"), cmdutil.Str(c, "database"), cmdutil.Str(c, "org")
	if org != "" && database != "" {
		return errors.New("--org and --database are different scopes: give one")
	}
	value, err := cmdutil.ReadSecretValue(cmdutil.IOOf(c), "Secret")
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
	fmt.Fprintf(c.Stdout(), "secret %q set\n", SecretRef(database+orgScope(org), name))
	return nil
}

func secretList(c *kli.CommandContext, o Opener) error {
	s, err := o.Open(c)
	if err != nil {
		return err
	}
	defer s.Close()
	t := s.t
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
	name, database, org := cmdutil.Str(c, "name"), cmdutil.Str(c, "database"), cmdutil.Str(c, "org")
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
		if proof, err = t.proof(cmdutil.IOOf(c)); err != nil {
			return err
		}
	}
	if err := t.adm.Secrets.Delete(t.ctx, name, q, withProof(proof)...); err != nil {
		if errors.As(err, &ae) && ae.Status == 404 {
			return fmt.Errorf("no secret named %q", SecretRef(database+orgScope(org), name))
		}
		return err
	}
	fmt.Fprintf(c.Stdout(), "secret %q deleted\n", SecretRef(database+orgScope(org), name))
	return nil
}

// SecretRef names a secret as function.yaml would: realm.NAME for the realm scope, NAME for a
// database's, and org:<id>.NAME for an organization's (scope is the database or orgScope).
func SecretRef(scope, name string) string {
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
	return authcore.OrgScope(org)
}
