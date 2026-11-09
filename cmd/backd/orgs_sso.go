package main

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// connectionRow is a connection as the admin API returns it.
type connectionRow struct {
	Provider        string   `json:"provider"`
	ClientID        string   `json:"client_id"`
	Tenant          string   `json:"tenant"`
	HostedDomain    string   `json:"hosted_domain"`
	EmailDomains    []string `json:"email_domains"`
	JoinRoles       []string `json:"join_roles"`
	Enabled         bool     `json:"enabled"`
	ClientSecretSet bool     `json:"client_secret_set"`
	RedirectURI     string   `json:"redirect_uri"`
	RedirectPath    string   `json:"redirect_path"`
}

func orgsSSO(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		Items []connectionRow `json:"items"`
	}
	if err := t.adm.Orgs.SSOList(t.ctx, cmdutil.Str(c, "id"), &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	if len(out.Items) == 0 {
		fmt.Fprintln(io.Stdout, "no connections")
		return nil
	}
	w := tabwriter.NewWriter(io.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tCLIENT ID\tLIMITED TO\tJOIN ROLES\tENABLED\tSECRET\tREGISTER THIS REDIRECT ADDRESS")
	for _, r := range out.Items {
		limit := r.Tenant + r.HostedDomain
		if len(r.EmailDomains) > 0 {
			limit += " (" + strings.Join(r.EmailDomains, ",") + ")"
		}
		secret := "missing"
		if r.ClientSecretSet {
			secret = "set"
		}
		redirect := r.RedirectURI
		if redirect == "" {
			redirect = r.RedirectPath + " (set BACKD_URL for the full address)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%s\t%s\n", r.Provider, r.ClientID, limit, strings.Join(r.JoinRoles, ","), r.Enabled, secret, redirect)
	}
	return w.Flush()
}

func orgsSSOSet(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{"client_id": cmdutil.Str(c, "client-id")}
	for flagName, key := range map[string]string{"tenant": "tenant", "hosted-domain": "hosted_domain"} {
		if v := cmdutil.Str(c, flagName); v != "" {
			body[key] = v
		}
	}
	for flagName, key := range map[string]string{"email-domains": "email_domains", "native-client-ids": "native_client_ids", "join-roles": "join_roles"} {
		if list := splitList(cmdutil.Str(c, flagName)); list != nil {
			body[key] = list
		}
	}
	if cmdutil.Flag(c, "disabled") {
		body["enabled"] = false
	}
	if !cmdutil.Flag(c, "keep-secret") {
		secret, err := cmdutil.ReadSecretValue(cmdutil.IOOf(c), "Client secret")
		if err != nil {
			return err
		}
		if secret == "" {
			return errors.New("the client secret must not be empty (--keep-secret leaves the stored one)")
		}
		body["client_secret"] = secret
	}
	var row connectionRow
	if err := t.adm.Orgs.SSOSet(t.ctx, cmdutil.Str(c, "id"), cmdutil.Str(c, "provider"), body, &row); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	fmt.Fprintf(io.Stdout, "%s connection of %s saved\n", row.Provider, cmdutil.Str(c, "id"))
	if row.RedirectURI != "" {
		fmt.Fprintf(io.Stdout, "register this redirect address with the provider: %s\n", row.RedirectURI)
	} else {
		fmt.Fprintf(io.Stdout, "register this path, after backd's public address, with the provider: %s\n", row.RedirectPath)
	}
	return nil
}

func orgsSSORequiredOff(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.adm.Orgs.Patch(t.ctx, cmdutil.Str(c, "id"), map[string]any{"sso_required": false}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "%s no longer requires its sign-in connection\n", cmdutil.Str(c, "id"))
	return nil
}

func orgsSSODelete(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.adm.Orgs.SSODelete(t.ctx, cmdutil.Str(c, "id"), cmdutil.Str(c, "provider")); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "%s connection of %s removed\n", cmdutil.Str(c, "provider"), cmdutil.Str(c, "id"))
	return nil
}
