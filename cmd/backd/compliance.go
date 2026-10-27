package main

import (
	"fmt"
	"strings"

	"github.com/fernandezvara/kli"
)

type complianceScope struct {
	Realm       string `json:"realm"`
	Auth        bool   `json:"auth"`
	Collections []struct {
		Database     string   `json:"database"`
		Collection   string   `json:"collection"`
		Export       string   `json:"export"`
		Owned        bool     `json:"owned"`
		Fields       []string `json:"fields"`
		Pull         []string `json:"pull"`
		Files        []string `json:"files"`
		Reason       string   `json:"reason"`
		Declared     bool     `json:"declared"`
		Organization bool     `json:"organization"`
	} `json:"collections"`
	System struct {
		Included []string `json:"included"`
		Excluded []string `json:"excluded"`
	} `json:"system"`
}

// complianceScopeCmd handles `backd compliance scope`: what a user's data
// export takes from each collection, and why anything is left out.
func complianceScopeCmd(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var sc complianceScope
	if err := t.call("GET", "_admin/compliance/scope", nil, nil, &sc); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []complianceScope{sc})
	}
	w := c.Stdout()
	if !sc.Auth {
		fmt.Fprintln(w, "this realm has auth disabled: it has no users, so there is no personal data to export")
		return nil
	}
	for _, e := range sc.Collections {
		name := e.Database + "/" + e.Collection
		if e.Export == "exclude" {
			fmt.Fprintf(w, "%-30s excluded: %s\n", name, e.Reason)
			continue
		}
		what := "whole documents"
		if e.Fields != nil {
			what = "fields " + strings.Join(e.Fields, ", ") + " (" + e.Reason + ")"
		}
		line := fmt.Sprintf("%-30s owned documents, %s", name, what)
		if len(e.Pull) > 0 {
			line += "; documents naming the user in " + strings.Join(e.Pull, ", ")
		}
		if len(e.Files) > 0 {
			line += "; files of " + strings.Join(e.Files, ", ")
		}
		if e.Organization {
			line += "\n  ! organization documents, exported whole: they may hold colleagues' data (narrow them with personal_data.fields)"
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, "\nsystem data included:")
	for _, s := range sc.System.Included {
		fmt.Fprintln(w, "  "+s)
	}
	fmt.Fprintln(w, "never included:")
	for _, s := range sc.System.Excluded {
		fmt.Fprintln(w, "  "+s)
	}
	return nil
}
