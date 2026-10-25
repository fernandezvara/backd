package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/fernandezvara/kli"
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

func ssoPath(id, provider string) string {
	p := "_admin/orgs/" + url.PathEscape(id) + "/sso"
	if provider != "" {
		p += "/" + url.PathEscape(provider)
	}
	return p
}

func orgsSSO(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		Items []connectionRow `json:"items"`
	}
	if err := t.call("GET", ssoPath(str(c, "id"), ""), nil, nil, &out); err != nil {
		return err
	}
	io := ioOf(c)
	if len(out.Items) == 0 {
		fmt.Fprintln(io.stdout, "no connections")
		return nil
	}
	w := tabwriter.NewWriter(io.stdout, 0, 4, 2, ' ', 0)
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
	body := map[string]any{"client_id": str(c, "client-id")}
	for flagName, key := range map[string]string{"tenant": "tenant", "hosted-domain": "hosted_domain"} {
		if v := str(c, flagName); v != "" {
			body[key] = v
		}
	}
	for flagName, key := range map[string]string{"email-domains": "email_domains", "native-client-ids": "native_client_ids", "join-roles": "join_roles"} {
		if list := splitList(str(c, flagName)); list != nil {
			body[key] = list
		}
	}
	if flag(c, "disabled") {
		body["enabled"] = false
	}
	if !flag(c, "keep-secret") {
		secret, err := readSecretValue(ioOf(c), "Client secret")
		if err != nil {
			return err
		}
		if secret == "" {
			return errors.New("the client secret must not be empty (--keep-secret leaves the stored one)")
		}
		body["client_secret"] = secret
	}
	var row connectionRow
	if err := t.call("PUT", ssoPath(str(c, "id"), str(c, "provider")), nil, body, &row); err != nil {
		return err
	}
	io := ioOf(c)
	fmt.Fprintf(io.stdout, "%s connection of %s saved\n", row.Provider, str(c, "id"))
	if row.RedirectURI != "" {
		fmt.Fprintf(io.stdout, "register this redirect address with the provider: %s\n", row.RedirectURI)
	} else {
		fmt.Fprintf(io.stdout, "register this path, after backd's public address, with the provider: %s\n", row.RedirectPath)
	}
	return nil
}

func orgsSSODelete(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.call("DELETE", ssoPath(str(c, "id"), str(c, "provider")), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "%s connection of %s removed\n", str(c, "provider"), str(c, "id"))
	return nil
}
