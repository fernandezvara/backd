package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/fernandezvara/kli"
)

// Organizations through the admin API (design/organizations.md §5.2).

type orgRow struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Slug      *string        `json:"slug"`
	Status    string         `json:"status"`
	Claims    map[string]any `json:"claims"`
	CreatedAt string         `json:"created_at"`
}

func (o orgRow) slug() string {
	if o.Slug == nil {
		return "-"
	}
	return *o.Slug
}

type orgMemberRow struct {
	User           string   `json:"user"`
	Email          string   `json:"email"`
	Roles          []string `json:"roles"`
	EffectiveRoles []struct {
		Role   string   `json:"role"`
		Origin []string `json:"origin"`
	} `json:"effective_roles"`
	JoinedAt string `json:"joined_at"`
}

// userIDByEmail finds a user of the realm by address.
func userIDByEmail(t *target, email string) (string, error) {
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := t.adm.Users.List(t.ctx, url.Values{"email": {email}}, &page); err != nil {
		return "", err
	}
	if len(page.Items) == 0 {
		return "", fmt.Errorf("user not found: %s", email)
	}
	return page.Items[0].ID, nil
}

func orgsList(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	q := url.Values{}
	if s := str(c, "status"); s != "" {
		q.Set("status", s)
	}
	var out struct {
		Items []orgRow `json:"items"`
	}
	if err := t.adm.Orgs.List(t.ctx, q, &out); err != nil {
		return err
	}
	io := ioOf(c)
	if len(out.Items) == 0 {
		fmt.Fprintln(io.stdout, "no organizations")
		return nil
	}
	w := tabwriter.NewWriter(io.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG\tSTATUS\tCREATED")
	for _, o := range out.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", o.ID, o.Name, o.slug(), o.Status, o.CreatedAt)
	}
	return w.Flush()
}

func orgsShow(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var o orgRow
	if err := t.adm.Orgs.Get(t.ctx, str(c, "id"), &o); err != nil {
		return err
	}
	io := ioOf(c)
	claims, _ := json.Marshal(o.Claims)
	fmt.Fprintf(io.stdout, "id       %s\nname     %s\nslug     %s\nstatus   %s\ncreated  %s\nclaims   %s\n", o.ID, o.Name, o.slug(), o.Status, o.CreatedAt, claims)
	return nil
}

func orgsCreate(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	owner, err := userIDByEmail(t, str(c, "owner"))
	if err != nil {
		return err
	}
	body := map[string]any{"name": str(c, "name"), "slug": str(c, "slug"), "owner": owner}
	var o orgRow
	if err := t.adm.Orgs.Create(t.ctx, body, &o); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "organization %s created (%s), owned by %s\n", o.ID, o.Name, str(c, "owner"))
	return nil
}

func orgsMembers(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		Items   []orgMemberRow `json:"items"`
		HasMore bool           `json:"has_more"`
	}
	if err := t.adm.Orgs.Members(t.ctx, str(c, "id"), url.Values{"limit": {"500"}}, &out); err != nil {
		return err
	}
	io := ioOf(c)
	w := tabwriter.NewWriter(io.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "USER\tEMAIL\tROLES\tEFFECTIVE")
	for _, m := range out.Items {
		var eff []string
		for _, e := range m.EffectiveRoles {
			eff = append(eff, e.Role+"("+strings.Join(e.Origin, "+")+")")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.User, m.Email, strings.Join(m.Roles, ","), strings.Join(eff, " "))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if out.HasMore {
		fmt.Fprintln(io.stderr, "note: only the first 500 members are shown")
	}
	return nil
}

func orgsAddMember(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	id, err := userIDByEmail(t, str(c, "email"))
	if err != nil {
		return err
	}
	body := map[string]any{"user": id}
	if r := str(c, "roles"); r != "" {
		body["roles"] = strings.Split(r, ",")
	}
	if err := t.adm.Orgs.AddMember(t.ctx, str(c, "id"), body); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "%s added to %s\n", str(c, "email"), str(c, "id"))
	return nil
}

func orgsAssignOwner(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	id, err := userIDByEmail(t, str(c, "email"))
	if err != nil {
		return err
	}
	if err := t.adm.Orgs.SetOwner(t.ctx, str(c, "id"), map[string]any{"user": id}); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "%s is now a manager of %s\n", str(c, "email"), str(c, "id"))
	return nil
}

func orgsClaimsSet(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(str(c, "claims")), &claims); err != nil {
		return fmt.Errorf("--claims must be a JSON object: %w", err)
	}
	if err := t.adm.Orgs.SetClaims(t.ctx, str(c, "id"), claims); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "claims of %s set\n", str(c, "id"))
	return nil
}

// orgsInvite creates an invitation: bound to an address (--email), to a user (--user) or open
// (--open). It prints the link, which is shown only now.
func orgsInvite(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{}
	email, user, open := str(c, "email"), str(c, "user"), flag(c, "open")
	switch {
	case open && (email != "" || user != ""):
		return fmt.Errorf("--open can't be combined with --email or --user")
	case open:
		body["binding"] = "none"
		if n := num(c, "uses"); n > 0 {
			body["max_uses"] = n
		}
	case email != "" && user != "":
		return fmt.Errorf("give --email or --user, not both")
	case email != "":
		body["binding"], body["email"] = "email", email
	case user != "":
		id, err := userIDByEmail(t, user)
		if err != nil {
			return err
		}
		body["binding"], body["user"] = "user", id
	default:
		return fmt.Errorf("say who is invited: --email, --user (an email of an existing user) or --open")
	}
	if r := str(c, "roles"); r != "" {
		body["roles"] = strings.Split(r, ",")
	}
	if e := str(c, "expires"); e != "" {
		body["expires_in"] = e
	}
	if flag(c, "send") {
		body["send"] = true
	}
	var out struct {
		ID        string  `json:"id"`
		Token     string  `json:"token"`
		Link      *string `json:"link"`
		ExpiresAt string  `json:"expires_at"`
	}
	if err := t.adm.Orgs.Invite(t.ctx, str(c, "id"), body, &out); err != nil {
		return err
	}
	io := ioOf(c)
	fmt.Fprintf(io.stdout, "invitation %s, valid until %s\n", out.ID, out.ExpiresAt)
	if out.Link != nil {
		fmt.Fprintf(io.stdout, "link   %s\n", *out.Link)
	}
	fmt.Fprintf(io.stdout, "token  %s\n", out.Token)
	return nil
}

func orgsInvitations(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			ID        string   `json:"id"`
			Binding   string   `json:"binding"`
			Status    string   `json:"status"`
			Email     string   `json:"email"`
			User      string   `json:"user"`
			Roles     []string `json:"roles"`
			Uses      int      `json:"uses"`
			MaxUses   int      `json:"max_uses"`
			ExpiresAt string   `json:"expires_at"`
		} `json:"items"`
	}
	if err := t.adm.Orgs.Invitations(t.ctx, str(c, "id"), &out); err != nil {
		return err
	}
	io := ioOf(c)
	w := tabwriter.NewWriter(io.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tBINDING\tFOR\tROLES\tUSES\tSTATUS\tEXPIRES")
	for _, i := range out.Items {
		who := i.Email
		if who == "" {
			who = i.User
		}
		if who == "" {
			who = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\n", i.ID, i.Binding, who, strings.Join(i.Roles, ","), i.Uses, i.MaxUses, i.Status, i.ExpiresAt)
	}
	return w.Flush()
}

func orgsRevokeInvitation(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.adm.Orgs.RevokeInvitation(t.ctx, str(c, "id"), str(c, "invitation")); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "invitation %s revoked\n", str(c, "invitation"))
	return nil
}

// dataTransfer moves one document to another organization (or out of its organization with
// --org none), and to a team of it (--team, or --team none), as an administrator.
func dataTransfer(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	org := any(str(c, "org"))
	if org == "none" {
		org = nil
	}
	body := map[string]any{"org": org}
	switch team := str(c, "team"); team {
	case "":
	case "none":
		body["team"] = nil
	default:
		body["team"] = team
	}
	if err := t.adm.Data.Transfer(t.ctx, str(c, "database"), str(c, "collection"), str(c, "id"), body, nil); err != nil {
		return err
	}
	to := str(c, "org")
	if to == "none" {
		to = "no organization"
	}
	if team := str(c, "team"); team != "" && team != "none" {
		to += ", team " + team
	}
	fmt.Fprintf(ioOf(c).stdout, "document %s moved to %s\n", str(c, "id"), to)
	return nil
}

// dataAssignOrg gives the documents of a collection that have no organization one.
func dataAssignOrg(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{"org": str(c, "org")}
	if w := str(c, "where"); w != "" {
		var where map[string]any
		if err := json.Unmarshal([]byte(w), &where); err != nil {
			return fmt.Errorf("--where must be a JSON object: %w", err)
		}
		body["where"] = where
	}
	q := url.Values{}
	if flag(c, "dry-run") {
		q.Set("dry_run", "true")
	}
	var out struct {
		Job     string `json:"job"`
		Matched int64  `json:"matched"`
	}
	if err := t.adm.Data.AssignOrg(t.ctx, str(c, "database"), str(c, "collection"), q, body, &out); err != nil {
		return err
	}
	io := ioOf(c)
	if flag(c, "dry-run") {
		fmt.Fprintf(io.stdout, "%d documents have no organization and match: nothing was changed\n", out.Matched)
		return nil
	}
	fmt.Fprintf(io.stdout, "job %s queued: %d documents will be given the organization (follow it with `backd functions jobs --realm %s --job %s`)\n", out.Job, out.Matched, str(c, "realm"), out.Job)
	return nil
}

func orgsOwned(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		Org         orgRow `json:"org"`
		Collections []struct {
			Database   string `json:"database"`
			Collection string `json:"collection"`
			Documents  int64  `json:"documents"`
			OnOrgDel   string `json:"on_org_delete"`
		} `json:"collections"`
		ToDelete int64 `json:"to_delete"`
		ToKeep   int64 `json:"to_keep"`
	}
	if err := t.adm.Orgs.Owned(t.ctx, str(c, "id"), &out); err != nil {
		return err
	}
	io := ioOf(c)
	fmt.Fprintf(io.stdout, "%s (%s): deleting it removes %d documents and leaves %d\n", out.Org.Name, out.Org.ID, out.ToDelete, out.ToKeep)
	w := tabwriter.NewWriter(io.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "COLLECTION\tDOCUMENTS\tON DELETE")
	for _, col := range out.Collections {
		fmt.Fprintf(w, "%s/%s\t%d\t%s\n", col.Database, col.Collection, col.Documents, col.OnOrgDel)
	}
	return w.Flush()
}

func orgsDelete(c *kli.CommandContext) error {
	if !flag(c, "yes") {
		return fmt.Errorf("deleting an organization ends everyone's access at once and removes the documents of its `delete` collections: see them first with `backd orgs owned`, then add --yes to confirm")
	}
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.adm.Orgs.Delete(t.ctx, str(c, "id")); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "organization %s deleted; its documents are being cleaned up in the background\n", str(c, "id"))
	return nil
}

func orgsSetSlug(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var o orgRow
	if err := t.adm.Orgs.SetSlug(t.ctx, str(c, "id"), map[string]any{"slug": str(c, "slug")}, &o); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "organization %s is now %s\n", o.ID, o.slug())
	return nil
}

func orgsReactivate(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if s := str(c, "slug"); s != "" {
		body["slug"] = s
	}
	if err := t.adm.Orgs.Reactivate(t.ctx, str(c, "id"), body); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "organization %s is active again, with no members: assign an owner with `backd orgs assign-owner`\n", str(c, "id"))
	return nil
}
