package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/fernandezvara/cli"

	"github.com/fernandezvara/backd/internal/registry"
)

// apiKey is an API key as the admin API returns it.
type apiKey struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Prefix     string   `json:"prefix"`
	Networks   []string `json:"networks"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt *string  `json:"last_used_at"`
	ExpiresAt  *string  `json:"expires_at"`
	Key        string   `json:"key"`
}

// keyOptions checks the create flags and adds them to the request body.
func keyOptions(c *cli.CommandContext, body map[string]any) error {
	body["role"] = str(c, "role")
	if v := str(c, "expires"); v != "" {
		if d, err := registry.ParseDuration(v); err != nil || d <= 0 {
			return usageErr(fmt.Errorf("--expires: want a positive duration such as 90d or 12h, got %q", v))
		}
		body["expires_in"] = v
	}
	if v := str(c, "networks"); v != "" {
		nets, err := registry.ParseNetworks(strings.Split(v, ","))
		if err != nil {
			return usageErr(fmt.Errorf("--networks: want comma-separated IP addresses or CIDR networks, got %q", v))
		}
		body["networks"] = nets.Strings()
	}
	return nil
}

func apikeyCreate(c *cli.CommandContext) error {
	body := map[string]any{"name": str(c, "name")}
	if err := keyOptions(c, body); err != nil {
		return err
	}
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var k apiKey
	if err := t.call("POST", "_admin/apikeys", nil, body, &k); err != nil {
		return err
	}
	uio := ioOf(c)
	fmt.Fprintln(uio.stdout, k.Key)
	msg := fmt.Sprintf("API key %q (role %s) created. Store it now as a secret: it can't be shown again.", k.Name, k.Role)
	if k.ExpiresAt != nil {
		msg += " It expires at " + *k.ExpiresAt + "."
	} else {
		msg += " Warning: it never expires; prefer --expires (for example 90d) and rotate keys before they expire."
	}
	fmt.Fprintln(uio.stderr, msg)
	return nil
}

func apikeyList(c *cli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var page struct{ Items []apiKey }
	if err := t.call("GET", "_admin/apikeys", nil, nil, &page); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tROLE\tKEY\tNETWORKS\tCREATED\tLAST USED\tEXPIRES")
	for _, k := range page.Items {
		nets := strings.Join(k.Networks, ",")
		if nets == "" {
			nets = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s…\t%s\t%s\t%s\t%s\n", k.Name, k.Role, k.Prefix, nets, k.CreatedAt, orDash(k.LastUsedAt), orDash(k.ExpiresAt))
	}
	return tw.Flush()
}

func apikeyRevoke(c *cli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	name := str(c, "name")
	var ae *apiError
	if err := t.call("DELETE", "_admin/apikeys/"+url.PathEscape(name), nil, nil, nil); err != nil {
		if errors.As(err, &ae) && ae.Status == 404 && ae.Message == "API key not found" {
			return fmt.Errorf("no API key named %q", name)
		}
		return err
	}
	fmt.Fprintf(c.Stdout(), "API key %q revoked\n", name)
	return nil
}

func orDash(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
