package admin

import (
	"fmt"
	"text/tabwriter"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cli/clicore"
	"github.com/fernandezvara/backd/internal/cmdutil"
)

// domainRow is a domain claim as the admin API returns it.
type domainRow struct {
	Domain       string `json:"domain"`
	State        string `json:"state"`
	Discoverable bool   `json:"discoverable"`
	LinkAccounts bool   `json:"link_accounts"`
	Reason       string `json:"reason"`
	Record       struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"record"`
}

func (r domainRow) printRecord(c *kli.CommandContext) {
	io := cmdutil.IOOf(c)
	fmt.Fprintf(io.Stdout, "publish this %s record to prove %s:\n  %s  %s\n", r.Record.Type, r.Domain, r.Record.Name, r.Record.Value)
}

func OrgsDomains(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		Items []domainRow `json:"items"`
	}
	if err := t.Adm.Orgs.DomainsList(t.Ctx, cmdutil.Str(c, "id"), &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	if len(out.Items) == 0 {
		fmt.Fprintln(io.Stdout, "no domains")
		return nil
	}
	w := tabwriter.NewWriter(io.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DOMAIN\tSTATE\tDISCOVERABLE\tLINKS ACCOUNTS\tRECORD TO PUBLISH")
	for _, r := range out.Items {
		record := ""
		if r.State != "verified" {
			record = r.Record.Name + " " + r.Record.Value
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%t\t%s\n", r.Domain, r.State, r.Discoverable, r.LinkAccounts, record)
	}
	return w.Flush()
}

func OrgsDomainAdd(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var out domainRow
	if err := t.Adm.Orgs.DomainClaim(t.Ctx, cmdutil.Str(c, "id"), map[string]any{"domain": cmdutil.Str(c, "domain")}, &out); err != nil {
		return err
	}
	out.printRecord(c)
	return nil
}

func OrgsDomainVerify(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var out domainRow
	if err := t.Adm.Orgs.DomainVerify(t.Ctx, cmdutil.Str(c, "id"), cmdutil.Str(c, "domain"), &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	if out.State == "verified" {
		fmt.Fprintf(io.Stdout, "%s is verified\n", out.Domain)
		return nil
	}
	fmt.Fprintf(io.Stdout, "%s is %s: the record was not proved (%s)\n", out.Domain, out.State, out.Reason)
	out.printRecord(c)
	return nil
}

func OrgsDomainRemove(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.Adm.Orgs.DomainRemove(t.Ctx, cmdutil.Str(c, "id"), cmdutil.Str(c, "domain")); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "%s released by %s\n", cmdutil.Str(c, "domain"), cmdutil.Str(c, "id"))
	return nil
}
