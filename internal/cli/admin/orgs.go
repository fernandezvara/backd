package admin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
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
func userIDByEmail(t *clicore.Target, email string) (string, error) {
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := t.Adm.Users.List(t.Ctx, url.Values{"email": {email}}, &page); err != nil {
		return "", err
	}
	if len(page.Items) == 0 {
		return "", fmt.Errorf("user not found: %s", email)
	}
	return page.Items[0].ID, nil
}

func OrgsList(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	q := url.Values{}
	if s := cmdutil.Str(c, "status"); s != "" {
		q.Set("status", s)
	}
	var out struct {
		Items []orgRow `json:"items"`
	}
	if err := t.Adm.Orgs.List(t.Ctx, q, &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	if len(out.Items) == 0 {
		fmt.Fprintln(io.Stdout, "no organizations")
		return nil
	}
	w := tabwriter.NewWriter(io.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG\tSTATUS\tCREATED")
	for _, o := range out.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", o.ID, o.Name, o.slug(), o.Status, o.CreatedAt)
	}
	return w.Flush()
}

func OrgsShow(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var o orgRow
	if err := t.Adm.Orgs.Get(t.Ctx, cmdutil.Str(c, "id"), &o); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	claims, _ := json.Marshal(o.Claims)
	fmt.Fprintf(io.Stdout, "id       %s\nname     %s\nslug     %s\nstatus   %s\ncreated  %s\nclaims   %s\n", o.ID, o.Name, o.slug(), o.Status, o.CreatedAt, claims)
	return nil
}

func OrgsCreate(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	owner, err := userIDByEmail(t, cmdutil.Str(c, "owner"))
	if err != nil {
		return err
	}
	body := map[string]any{"name": cmdutil.Str(c, "name"), "slug": cmdutil.Str(c, "slug"), "owner": owner}
	var o orgRow
	if err := t.Adm.Orgs.Create(t.Ctx, body, &o); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "organization %s created (%s), owned by %s\n", o.ID, o.Name, cmdutil.Str(c, "owner"))
	return nil
}

func OrgsMembers(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var out struct {
		Items   []orgMemberRow `json:"items"`
		HasMore bool           `json:"has_more"`
	}
	if err := t.Adm.Orgs.Members(t.Ctx, cmdutil.Str(c, "id"), url.Values{"limit": {"500"}}, &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	w := tabwriter.NewWriter(io.Stdout, 0, 4, 2, ' ', 0)
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
		fmt.Fprintln(io.Stderr, "note: only the first 500 members are shown")
	}
	return nil
}

func OrgsAddMember(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	id, err := userIDByEmail(t, cmdutil.Str(c, "email"))
	if err != nil {
		return err
	}
	body := map[string]any{"user": id}
	if r := cmdutil.Str(c, "roles"); r != "" {
		body["roles"] = strings.Split(r, ",")
	}
	if err := t.Adm.Orgs.AddMember(t.Ctx, cmdutil.Str(c, "id"), body); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "%s added to %s\n", cmdutil.Str(c, "email"), cmdutil.Str(c, "id"))
	return nil
}

func OrgsAssignOwner(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	id, err := userIDByEmail(t, cmdutil.Str(c, "email"))
	if err != nil {
		return err
	}
	if err := t.Adm.Orgs.SetOwner(t.Ctx, cmdutil.Str(c, "id"), map[string]any{"user": id}); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "%s is now a manager of %s\n", cmdutil.Str(c, "email"), cmdutil.Str(c, "id"))
	return nil
}

func OrgsClaimsSet(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(cmdutil.Str(c, "claims")), &claims); err != nil {
		return fmt.Errorf("--claims must be a JSON object: %w", err)
	}
	if err := t.Adm.Orgs.SetClaims(t.Ctx, cmdutil.Str(c, "id"), claims); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "claims of %s set\n", cmdutil.Str(c, "id"))
	return nil
}

// orgsInvite creates an invitation: bound to an address (--email), to a user (--user) or open
// (--open). It prints the link, which is shown only now.
func OrgsInvite(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{}
	email, user, open := cmdutil.Str(c, "email"), cmdutil.Str(c, "user"), cmdutil.Flag(c, "open")
	switch {
	case open && (email != "" || user != ""):
		return fmt.Errorf("--open can't be combined with --email or --user")
	case open:
		body["binding"] = "none"
		if n := cmdutil.Num(c, "uses"); n > 0 {
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
	if r := cmdutil.Str(c, "roles"); r != "" {
		body["roles"] = strings.Split(r, ",")
	}
	if e := cmdutil.Str(c, "expires"); e != "" {
		body["expires_in"] = e
	}
	if cmdutil.Flag(c, "send") {
		body["send"] = true
	}
	var out struct {
		ID        string  `json:"id"`
		Token     string  `json:"token"`
		Link      *string `json:"link"`
		ExpiresAt string  `json:"expires_at"`
	}
	if err := t.Adm.Orgs.Invite(t.Ctx, cmdutil.Str(c, "id"), body, &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	fmt.Fprintf(io.Stdout, "invitation %s, valid until %s\n", out.ID, out.ExpiresAt)
	if out.Link != nil {
		fmt.Fprintf(io.Stdout, "link   %s\n", *out.Link)
	}
	fmt.Fprintf(io.Stdout, "token  %s\n", out.Token)
	return nil
}

func OrgsInvitations(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
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
	if err := t.Adm.Orgs.Invitations(t.Ctx, cmdutil.Str(c, "id"), &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	w := tabwriter.NewWriter(io.Stdout, 0, 4, 2, ' ', 0)
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

func OrgsRevokeInvitation(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.Adm.Orgs.RevokeInvitation(t.Ctx, cmdutil.Str(c, "id"), cmdutil.Str(c, "invitation")); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "invitation %s revoked\n", cmdutil.Str(c, "invitation"))
	return nil
}

// dataTransfer moves one document to another organization (or out of its organization with
// --org none), and to a team of it (--team, or --team none), as an administrator.
func DataTransfer(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	org := any(cmdutil.Str(c, "org"))
	if org == "none" {
		org = nil
	}
	body := map[string]any{"org": org}
	switch team := cmdutil.Str(c, "team"); team {
	case "":
	case "none":
		body["team"] = nil
	default:
		body["team"] = team
	}
	if err := t.Adm.Data.Transfer(t.Ctx, cmdutil.Str(c, "database"), cmdutil.Str(c, "collection"), cmdutil.Str(c, "id"), body, nil); err != nil {
		return err
	}
	to := cmdutil.Str(c, "org")
	if to == "none" {
		to = "no organization"
	}
	if team := cmdutil.Str(c, "team"); team != "" && team != "none" {
		to += ", team " + team
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "document %s moved to %s\n", cmdutil.Str(c, "id"), to)
	return nil
}

// dataAssignOrg gives the documents of a collection that have no organization one.
func DataAssignOrg(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{"org": cmdutil.Str(c, "org")}
	if w := cmdutil.Str(c, "where"); w != "" {
		var where map[string]any
		if err := json.Unmarshal([]byte(w), &where); err != nil {
			return fmt.Errorf("--where must be a JSON object: %w", err)
		}
		body["where"] = where
	}
	q := url.Values{}
	if cmdutil.Flag(c, "dry-run") {
		q.Set("dry_run", "true")
	}
	var out struct {
		Job     string `json:"job"`
		Matched int64  `json:"matched"`
	}
	if err := t.Adm.Data.AssignOrg(t.Ctx, cmdutil.Str(c, "database"), cmdutil.Str(c, "collection"), q, body, &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	if cmdutil.Flag(c, "dry-run") {
		fmt.Fprintf(io.Stdout, "%d documents have no organization and match: nothing was changed\n", out.Matched)
		return nil
	}
	fmt.Fprintf(io.Stdout, "job %s queued: %d documents will be given the organization (follow it with `backd functions jobs --realm %s --job %s`)\n", out.Job, out.Matched, cmdutil.Str(c, "realm"), out.Job)
	return nil
}

func OrgsOwned(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
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
	if err := t.Adm.Orgs.Owned(t.Ctx, cmdutil.Str(c, "id"), &out); err != nil {
		return err
	}
	io := cmdutil.IOOf(c)
	fmt.Fprintf(io.Stdout, "%s (%s): deleting it removes %d documents and leaves %d\n", out.Org.Name, out.Org.ID, out.ToDelete, out.ToKeep)
	w := tabwriter.NewWriter(io.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "COLLECTION\tDOCUMENTS\tON DELETE")
	for _, col := range out.Collections {
		fmt.Fprintf(w, "%s/%s\t%d\t%s\n", col.Database, col.Collection, col.Documents, col.OnOrgDel)
	}
	return w.Flush()
}

func OrgsDelete(c *kli.CommandContext) error {
	if !cmdutil.Flag(c, "yes") {
		return fmt.Errorf("deleting an organization ends everyone's access at once and removes the documents of its `delete` collections: see them first with `backd orgs owned`, then add --yes to confirm")
	}
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	if err := t.Adm.Orgs.Delete(t.Ctx, cmdutil.Str(c, "id")); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "organization %s deleted; its documents are being cleaned up in the background\n", cmdutil.Str(c, "id"))
	return nil
}

func OrgsSetSlug(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	var o orgRow
	if err := t.Adm.Orgs.SetSlug(t.Ctx, cmdutil.Str(c, "id"), map[string]any{"slug": cmdutil.Str(c, "slug")}, &o); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "organization %s is now %s\n", o.ID, o.slug())
	return nil
}

func OrgsReactivate(c *kli.CommandContext) error {
	t, err := clicore.TargetOf(c, false)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if s := cmdutil.Str(c, "slug"); s != "" {
		body["slug"] = s
	}
	if err := t.Adm.Orgs.Reactivate(t.Ctx, cmdutil.Str(c, "id"), body); err != nil {
		return err
	}
	fmt.Fprintf(cmdutil.IOOf(c).Stdout, "organization %s is active again, with no members: assign an owner with `backd orgs assign-owner`\n", cmdutil.Str(c, "id"))
	return nil
}
