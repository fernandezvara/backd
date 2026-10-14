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
	if err := t.call("GET", "_admin/users", url.Values{"email": {email}}, nil, &page); err != nil {
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
	if err := t.call("GET", "_admin/orgs", q, nil, &out); err != nil {
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
	if err := t.call("GET", "_admin/orgs/"+url.PathEscape(str(c, "id")), nil, nil, &o); err != nil {
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
	body := map[string]any{"name": str(c, "name"), "owner": owner}
	if s := str(c, "slug"); s != "" {
		body["slug"] = s
	}
	var o orgRow
	if err := t.call("POST", "_admin/orgs", nil, body, &o); err != nil {
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
	if err := t.call("GET", "_admin/orgs/"+url.PathEscape(str(c, "id"))+"/members", url.Values{"limit": {"500"}}, nil, &out); err != nil {
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
	if err := t.call("POST", "_admin/orgs/"+url.PathEscape(str(c, "id"))+"/members", nil, body, nil); err != nil {
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
	if err := t.call("POST", "_admin/orgs/"+url.PathEscape(str(c, "id"))+"/owner", nil, map[string]any{"user": id}, nil); err != nil {
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
	if err := t.call("PUT", "_admin/orgs/"+url.PathEscape(str(c, "id"))+"/claims", nil, claims, nil); err != nil {
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
	if err := t.call("POST", "_admin/orgs/"+url.PathEscape(str(c, "id"))+"/invitations", nil, body, &out); err != nil {
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
	if err := t.call("GET", "_admin/orgs/"+url.PathEscape(str(c, "id"))+"/invitations", nil, nil, &out); err != nil {
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
	if err := t.call("DELETE", "_admin/orgs/"+url.PathEscape(str(c, "id"))+"/invitations/"+url.PathEscape(str(c, "invitation")), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(ioOf(c).stdout, "invitation %s revoked\n", str(c, "invitation"))
	return nil
}
