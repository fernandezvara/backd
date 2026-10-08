package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/kli"
	"golang.org/x/term"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// adminUser is a user as the admin API returns it.
type adminUser struct {
	ID            string   `json:"id"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Disabled      bool     `json:"disabled"`
	Roles         []string `json:"roles"`
	AdminNetworks []string `json:"admin_networks"`
	LoginNetworks []string `json:"login_networks"`
	CreatedAt     string   `json:"created_at"`
}

// userCtx is what a user subcommand works with.
type userCtx struct {
	t     *target
	cmd   *kli.CommandContext
	email string
	uio   userIO
	local *registry.RealmSettings // the realm's settings, when CONFIG_DIR has it
}

type userIO struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// find looks a user up by email.
func (c *userCtx) find() (adminUser, error) {
	var page struct{ Items []adminUser }
	if err := c.t.call("GET", "_admin/users", url.Values{"email": {c.email}}, nil, &page); err != nil {
		return adminUser{}, err
	}
	if len(page.Items) == 0 {
		return adminUser{}, fmt.Errorf("user not found: %s", c.email)
	}
	return page.Items[0], nil
}

// onUser finds the user and sends a request to one of their resources.
func (c *userCtx) onUser(method, sub string, body any) (adminUser, error) {
	u, err := c.find()
	if err != nil {
		return u, err
	}
	var out adminUser
	err = c.t.call(method, "_admin/users/"+url.PathEscape(u.ID)+sub, nil, body, &out)
	if out.ID == "" {
		out = u
	}
	return out, err
}

func userCreate(c *userCtx) error {
	body := map[string]any{"email": c.email}
	noPassword := flag(c.cmd, "no-password")
	if !noPassword {
		p, err := readSecret(c.uio, true)
		if err != nil {
			return err
		}
		body["password"] = p
	}
	var u adminUser
	if err := c.t.call("POST", "_admin/users", nil, body, &u); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "created user %s (id %s)\n", u.Email, u.ID)
	if noPassword {
		fmt.Fprintln(c.uio.stdout, "the user has no password yet; set one with `backd user set-password`")
	}
	return nil
}

func userList(c *userCtx) error {
	tw := tabwriter.NewWriter(c.uio.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tID\tVERIFIED\tDISABLED\tROLES\tCREATED")
	for after := ""; ; {
		var page struct {
			Items      []adminUser
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := c.t.call("GET", "_admin/users", q, nil, &page); err != nil {
			return err
		}
		for _, x := range page.Items {
			fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%s\t%s\n", x.Email, x.ID, x.EmailVerified, x.Disabled,
				strings.Join(x.Roles, ","), x.CreatedAt)
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		after = page.NextCursor
	}
	return tw.Flush()
}

func userSetPassword(c *userCtx) error {
	p, err := readSecret(c.uio, true)
	if err != nil {
		return err
	}
	u, err := c.onUser("POST", "/password", map[string]string{"password": p})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "password set for %s; all of their sessions were revoked\n", u.Email)
	return nil
}

func userChangeEmail(c *userCtx) error {
	u, err := c.onUser("POST", "/email", map[string]string{"email": str(c.cmd, "new-email")})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "email changed to %s; all of their sessions were revoked, and both addresses were told (the old one can undo it for 7 days)\n", u.Email)
	return nil
}

// userPatch returns a command that changes one field of the user and
// prints message with the user's email.
func userPatch(body map[string]bool, message string) func(*userCtx) error {
	return func(c *userCtx) error {
		u, err := c.onUser("PATCH", "", body)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.uio.stdout, message, u.Email)
		return nil
	}
}

func userDelete(c *userCtx) error {
	if !flag(c.cmd, "yes") {
		return errors.New("deleting a user can't be undone; add --yes to confirm")
	}
	wait, err := time.ParseDuration(str(c.cmd, "wait"))
	if err != nil {
		return fmt.Errorf("--wait: %w (such as 2m, or 0s to not wait)", err)
	}
	u, err := c.find()
	if err != nil {
		return err
	}
	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := c.t.call("DELETE", "_admin/users/"+url.PathEscape(u.ID), nil, nil, &job); err != nil {
		return err
	}
	out := c.uio.stdout
	fmt.Fprintf(out, "erasing %s (id %s): the account is a tombstone now, with no sign-in methods or sessions; job %s applies the collections' policies\n", u.Email, u.ID, job.ID)
	if wait <= 0 {
		fmt.Fprintln(out, "not waiting: a worker finishes it (see `backd audit --action user.erased`)")
		return nil
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		var jobs struct {
			Items []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"items"`
		}
		if err := c.t.call("GET", "_admin/jobs", url.Values{"origin": {"backd:account.erase"}, "limit": {"100"}}, nil, &jobs); err != nil {
			return err
		}
		for _, j := range jobs.Items {
			if j.ID == job.ID && j.Status == "done" {
				return printErased(c, u.ID)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Fprintf(out, "still running after %s: a worker (backd worker, or serve --with-worker) must be up to finish it; check `backd audit --action user.erased`\n", wait)
	return nil
}

// printErased prints the counts of a finished erase from its audit record.
func printErased(c *userCtx, id string) error {
	var page struct {
		Items []struct {
			Details struct {
				Counts         map[string]int64 `json:"counts"`
				NeedsAttention bool             `json:"needs_attention"`
				Error          string           `json:"error"`
			} `json:"details"`
		} `json:"items"`
	}
	if err := c.t.call("GET", "_admin/audit", url.Values{"action": {"user.erased"}, "target": {"user:" + id}, "limit": {"1"}}, nil, &page); err != nil {
		return err
	}
	out := c.uio.stdout
	if len(page.Items) == 0 {
		fmt.Fprintln(out, "done")
		return nil
	}
	d := page.Items[0].Details
	if d.NeedsAttention {
		return fmt.Errorf("the erase failed and needs attention: %s\nrepeat `backd user delete` once the problem is fixed to resume it", d.Error)
	}
	if len(d.Counts) == 0 {
		fmt.Fprintln(out, "done: no collection declares a policy that touched anything")
		return nil
	}
	fmt.Fprintln(out, "done:")
	for _, k := range slices.Sorted(maps.Keys(d.Counts)) {
		fmt.Fprintf(out, "  %s: %d\n", k, d.Counts[k])
	}
	return nil
}

// ownedReport is what GET /_admin/users/{id}/owned answers.
type ownedReport struct {
	User struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"user"`
	Collections []struct {
		Database   string           `json:"database"`
		Collection string           `json:"collection"`
		Action     *string          `json:"action"`
		Owned      int64            `json:"owned"`
		Remove     []string         `json:"remove"`
		Replace    []string         `json:"replace"`
		Pull       map[string]int64 `json:"pull"`
		Unset      map[string]int64 `json:"unset"`
	} `json:"collections"`
	WithoutPolicy []string `json:"without_policy"`
}

// userOwned prints what erasing the user would do, per collection with a policy.
func userOwned(c *userCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	var report ownedReport
	if err := c.t.call("GET", "_admin/users/"+url.PathEscape(u.ID)+"/owned", nil, nil, &report); err != nil {
		return err
	}
	out := c.uio.stdout
	fmt.Fprintf(out, "%s (id %s): %s\n", u.Email, report.User.ID, report.User.Status)
	if len(report.Collections) == 0 {
		fmt.Fprintln(out, "no collection declares a policy: an erase leaves every document alone")
	}
	for _, col := range report.Collections {
		action := "keeps the user's documents"
		if col.Action != nil {
			action = fmt.Sprintf("%s: %d owned document(s)", *col.Action, col.Owned)
			if len(col.Remove)+len(col.Replace) > 0 {
				action += fmt.Sprintf(" (remove %s; replace %s)", strings.Join(col.Remove, ","), strings.Join(col.Replace, ","))
			}
		}
		fmt.Fprintf(out, "%s.%s  %s\n", col.Database, col.Collection, action)
		for _, kind := range []struct {
			name   string
			counts map[string]int64
		}{{"pull", col.Pull}, {"unset", col.Unset}} {
			for _, field := range slices.Sorted(maps.Keys(kind.counts)) {
				fmt.Fprintf(out, "    %s %s: %d document(s) hold the user\n", kind.name, field, kind.counts[field])
			}
		}
	}
	if len(report.WithoutPolicy) > 0 {
		fmt.Fprintf(out, "left alone (no policy): %s\n", strings.Join(report.WithoutPolicy, ", "))
	}
	return nil
}

// localSettings returns the realm's settings from CONFIG_DIR, if it is set
// and loads: run next to the server's config (in its container, say), the
// CLI can tell which assignments realm.yaml makes.
func localSettings(realm string, getenv func(string) string) *registry.RealmSettings {
	dir := getenv("CONFIG_DIR")
	if dir == "" {
		return nil
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return nil
	}
	if rl, ok := reg.Realms[realm]; ok {
		return &rl.Settings
	}
	return nil
}

func userRole(c *userCtx, verb, role string) error {
	method := "PUT"
	if verb == "remove" {
		method = "DELETE"
	}
	u, err := c.onUser(method, "/roles/"+url.PathEscape(role), nil)
	if err != nil {
		return err
	}
	realm := c.t.realm
	if verb == "add" {
		fmt.Fprintf(c.uio.stdout, "role %q assigned to %s\n", role, u.Email)
	} else {
		fmt.Fprintf(c.uio.stdout, "role %q removed from %s\n", role, u.Email)
	}
	switch {
	case c.local == nil:
		fmt.Fprintf(c.uio.stderr, "note: role assignments that %s/realm.yaml doesn't list under roles.%s.users live only in the database, and those it lists come back at the next start.\n", realm, role)
	case verb == "add" && !slices.Contains(c.local.Roles[role].Users, u.Email):
		fmt.Fprintf(c.uio.stderr, "warning: this assignment is stored only in the database. If the realm is rebuilt from its config, it won't come back.\n"+
			"To keep it, list %s under roles.%s.users in %s/realm.yaml.\n", u.Email, role, realm)
	case verb == "remove" && slices.Contains(c.local.Roles[role].Users, u.Email):
		fmt.Fprintf(c.uio.stderr, "warning: %s/realm.yaml assigns this role to %s, so it will be assigned again at the next `backd serve` or `backd provision`.\n"+
			"Remove %s from roles.%s.users in realm.yaml too.\n", realm, u.Email, u.Email, role)
	}
	return nil
}

// userNetworks shows or changes a user's network restrictions.
func userNetworks(c *userCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	admin, login := u.AdminNetworks, u.LoginNetworks
	changed := false
	for _, n := range []struct {
		flag string
		list *[]string
	}{{"admin", &admin}, {"login", &login}} {
		v := str(c.cmd, n.flag)
		if v == "" {
			continue
		}
		changed = true
		*n.list = []string{}
		if v != "none" {
			nets, err := registry.ParseNetworks(strings.Split(v, ","))
			if err != nil {
				return usageErr(fmt.Errorf("--%s: want comma-separated IP addresses or CIDR networks, or none; got %q", n.flag, v))
			}
			*n.list = nets.Strings()
		}
	}
	if changed {
		body := map[string][]string{"admin_networks": nonNil(admin), "login_networks": nonNil(login)}
		if err := c.t.call("PUT", "_admin/users/"+url.PathEscape(u.ID)+"/networks", nil, body, &u); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.uio.stdout, "%s\n  admin networks: %s\n  login networks: %s\n", u.Email, orAny(u.AdminNetworks), orAny(u.LoginNetworks))
	if changed {
		if c.local == nil {
			fmt.Fprintf(c.uio.stderr, "note: if %s/realm.yaml sets networks for %s, they are restored at the next start.\n", c.t.realm, u.Email)
		} else if _, ok := c.local.UserNetworks[u.Email]; ok {
			fmt.Fprintf(c.uio.stderr, "warning: %s/realm.yaml sets networks for %s, so they will be restored at the next `backd serve` or `backd provision`. Change them in realm.yaml too.\n", c.t.realm, u.Email)
		}
	}
	return nil
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func orAny(list []string) string {
	if len(list) == 0 {
		return "any"
	}
	return strings.Join(list, ", ")
}

// users returns the user service of an auth-enabled, provisioned realm.
func (a *app) users(ctx context.Context, realm string) (*auth.Users, error) {
	rl, ok := a.reg.Realms[realm]
	if !ok {
		return nil, fmt.Errorf("realm %q is not configured", realm)
	}
	if !rl.Settings.AuthEnabled {
		return nil, fmt.Errorf("realm %q has auth disabled, so it has no users", realm)
	}
	if err := a.provisioner().VerifySystem(ctx, realm); err != nil {
		return nil, err
	}
	return &auth.Users{
		Store:    mongodb.NewAuthStore(a.client, realm),
		Hasher:   auth.NewHasher(a.cfg.PasswordHashConcurrency, auth.DefaultArgon2Params),
		Settings: rl.Settings,
		Realm:    realm,
		Log:      a.log,
	}, nil
}

// readSecret reads a password from the terminal without echo, twice when
// confirm is set; otherwise it reads the first line of stdin.
func readSecret(uio userIO, confirm bool) (string, error) {
	if f, ok := uio.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(uio.stderr, "Password: ")
		p1, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(uio.stderr)
		if err != nil {
			return "", err
		}
		if !confirm {
			return string(p1), nil
		}
		fmt.Fprint(uio.stderr, "Repeat password: ")
		p2, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(uio.stderr)
		if err != nil {
			return "", err
		}
		if string(p1) != string(p2) {
			return "", errors.New("passwords don't match")
		}
		return string(p1), nil
	}
	line, err := bufio.NewReader(uio.stdin).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("no password on standard input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
