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
		p, err := readSecret(c.uio, true, "Password")
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
	p, err := readSecret(c.uio, true, "Password")
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

func userExpirePassword(c *userCtx) error {
	body := map[string]string{}
	if r := str(c.cmd, "reason"); r != "" {
		body["reason"] = r
	}
	u, err := c.onUser("POST", "/expire-password", body)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "the password of %s expired; they can sign in again after a password reset or a new password from an administrator (their sessions were kept)\n", u.Email)
	return nil
}

func userExpirePasswords(c *userCtx) error {
	if !flag(c.cmd, "yes") {
		msg := "this expires the password of every account in the realm and ends every session"
		if flag(c.cmd, "keep-sessions") {
			msg = "this expires the password of every account in the realm"
		}
		return errors.New(msg + ": add --yes to confirm")
	}
	body := map[string]any{"keep_sessions": flag(c.cmd, "keep-sessions"), "notify": flag(c.cmd, "notify")}
	if r := str(c.cmd, "reason"); r != "" {
		body["reason"] = r
	}
	var out struct {
		ValidAfter string `json:"valid_after"`
		Accounts   int64
		Sessions   int64
		NotifyJob  string `json:"notify_job"`
	}
	proof, err := c.t.proof(c.uio)
	if err != nil {
		return err
	}
	if _, err := c.t.callHeaders("POST", "_admin/passwords/expire", nil, body, &out, proof); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "every password set before %s expired: %d accounts with a password, %d sessions ended\nusers choose a new password with a reset (a sign-in with the old one queues the email), or you set one with `backd user set-password`\n", out.ValidAfter, out.Accounts, out.Sessions)
	if out.NotifyJob != "" {
		fmt.Fprintf(c.uio.stdout, "telling the users by email: job %s (follow it with `backd functions jobs`; `backd user notify-expired` starts it again if it is cancelled or fails)\n", out.NotifyJob)
	}
	return nil
}

func userNotifyExpired(c *userCtx) error {
	var out struct {
		ID string `json:"id"`
	}
	proof, err := c.t.proof(c.uio)
	if err != nil {
		return err
	}
	if _, err := c.t.callHeaders("POST", "_admin/passwords/notify", nil, nil, &out, proof); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "telling the users whose password expired: job %s; users already told are skipped (follow it with `backd functions jobs`)\n", out.ID)
	return nil
}

func userRevokeSessions(c *userCtx) error {
	if !flag(c.cmd, "all") {
		return usageErr(errors.New("--all is required: it ends the sessions of every user (to end one user's, set a password or disable them)"))
	}
	if !flag(c.cmd, "yes") {
		return errors.New("this ends every session in the realm: add --yes to confirm")
	}
	var out struct{ Sessions int64 }
	proof, err := c.t.proof(c.uio)
	if err != nil {
		return err
	}
	if _, err := c.t.callHeaders("DELETE", "_admin/sessions", nil, nil, &out, proof); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "%d sessions ended\n", out.Sessions)
	return nil
}

func userResetLink(c *userCtx) error {
	body := map[string]string{}
	if r := str(c.cmd, "redirect-to"); r != "" {
		body["redirect_to"] = r
	}
	u, err := c.find()
	if err != nil {
		return err
	}
	proof, err := c.t.proof(c.uio)
	if err != nil {
		return err
	}
	var out struct {
		Link      string
		ExpiresAt string `json:"expires_at"`
	}
	if _, err := c.t.callHeaders("POST", "_admin/users/"+url.PathEscape(u.ID)+"/reset-link", nil, body, &out, proof); err != nil {
		return err
	}
	fmt.Fprintln(c.uio.stdout, out.Link)
	fmt.Fprintf(c.uio.stderr, "reset link for %s, shown once; it works once and expires at %s. Hand it over by a channel you trust: whoever holds it can set the password. Nothing was sent.\n", u.Email, out.ExpiresAt)
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
	Organizations []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		LastManager bool   `json:"last_manager"`
	} `json:"organizations"`
	Kept []struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		Documents  int64  `json:"documents"`
	} `json:"kept_in_organizations"`
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
	for _, k := range report.Kept {
		fmt.Fprintf(out, "%s.%s  %d document(s) belong to an organization: they stay with it, the owner cleared\n", k.Database, k.Collection, k.Documents)
	}
	for _, o := range report.Organizations {
		if o.LastManager {
			fmt.Fprintf(out, "organization %s (%s): the user is the only manager: it becomes orphaned until an administrator assigns one\n", o.Name, o.ID)
		} else {
			fmt.Fprintf(out, "organization %s (%s): the user is a manager, and others remain\n", o.Name, o.ID)
		}
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
func readSecret(uio userIO, confirm bool, prompt string) (string, error) {
	if f, ok := uio.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(uio.stderr, "%s: ", prompt)
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

// readSecretValue reads the value of a secret: a hidden prompt on a terminal, and all of
// standard input otherwise, so a file (a service account's JSON, a .p8 key) can be piped in.
// Only the line ending at the very end is dropped.
func readSecretValue(uio userIO, prompt string) (string, error) {
	if f, ok := uio.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return readSecret(uio, false, prompt)
	}
	data, err := io.ReadAll(io.LimitReader(uio.stdin, 1<<20))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("no secret on standard input")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// userIdentities lists the user's sign-in methods.
func userIdentities(c *userCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			Provider      string  `json:"provider"`
			Email         string  `json:"email"`
			EmailVerified bool    `json:"email_verified"`
			CreatedAt     string  `json:"created_at"`
			LastUsedAt    *string `json:"last_used_at"`
		} `json:"items"`
	}
	if err := c.t.call("GET", "_admin/users/"+url.PathEscape(u.ID)+"/identities", nil, nil, &out); err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.uio.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tEMAIL\tVERIFIED\tLINKED\tLAST USED")
	for _, i := range out.Items {
		last := "-"
		if i.LastUsedAt != nil {
			last = *i.LastUsedAt
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\n", i.Provider, i.Email, i.EmailVerified, i.CreatedAt, last)
	}
	return w.Flush()
}

// userUnlinkIdentity removes one of the user's sign-in methods.
func userUnlinkIdentity(c *userCtx) error {
	provider := str(c.cmd, "provider")
	u, err := c.find()
	if err != nil {
		return err
	}
	if err := c.t.call("DELETE", "_admin/users/"+url.PathEscape(u.ID)+"/identities/"+url.PathEscape(provider), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "%s can no longer sign in with %s\n", u.Email, provider)
	return nil
}

// userMFAStatus shows one user's second factor, or who must still set one up.
func userMFAStatus(c *userCtx) error {
	c.email = str(c.cmd, "email")
	if c.email != "" {
		u, err := c.find()
		if err != nil {
			return err
		}
		var st struct {
			Enabled           bool `json:"enabled"`
			Available         bool `json:"available"`
			Required          bool `json:"required"`
			RecoveryCodesLeft int  `json:"recovery_codes_left"`
		}
		if err := c.t.call("GET", "_admin/users/"+url.PathEscape(u.ID)+"/mfa", nil, nil, &st); err != nil {
			return err
		}
		fmt.Fprintf(c.uio.stdout, "%s: second factor %s; required by role: %t; recovery codes left: %d\n", u.Email, onOff(st.Enabled), st.Required, st.RecoveryCodesLeft)
		return nil
	}
	var out struct {
		Enabled      bool     `json:"enabled"`
		RequiredAll  bool     `json:"required_all"`
		RequiredRole []string `json:"required_roles"`
		Users        []struct {
			Email string   `json:"email"`
			Roles []string `json:"roles"`
		} `json:"users_without_mfa"`
		Total int `json:"total_without_mfa"`
	}
	if err := c.t.call("GET", "_admin/mfa", nil, nil, &out); err != nil {
		return err
	}
	switch {
	case out.RequiredAll:
		fmt.Fprintln(c.uio.stdout, "MFA is required for every user")
	case len(out.RequiredRole) > 0:
		fmt.Fprintf(c.uio.stdout, "MFA is required for the roles: %s\n", strings.Join(out.RequiredRole, ", "))
	default:
		fmt.Fprintln(c.uio.stdout, "MFA is required for no role (mfa.required_roles in realm.yaml)")
		return nil
	}
	if out.Total == 0 {
		fmt.Fprintln(c.uio.stdout, "every user who must use it has set it up")
		return nil
	}
	fmt.Fprintf(c.uio.stdout, "%d user(s) have none yet and will be asked at their next sign-in:\n", out.Total)
	for _, u := range out.Users {
		fmt.Fprintf(c.uio.stdout, "  %s (%s)\n", u.Email, strings.Join(u.Roles, ", "))
	}
	if out.Total > len(out.Users) {
		fmt.Fprintf(c.uio.stdout, "  … and %d more\n", out.Total-len(out.Users))
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// userMFAReset removes a user's second factor through the admin API.
func userMFAReset(c *userCtx) error {
	if !flag(c.cmd, "yes") {
		return errors.New("resetting a second factor ends all of the user's sessions and can't be undone: add --yes to confirm")
	}
	u, err := c.find()
	if err != nil {
		return err
	}
	if err := c.t.call("POST", "_admin/users/"+url.PathEscape(u.ID)+"/mfa/reset", nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(c.uio.stdout, "second factor of %s removed; their sessions ended\n", u.Email)
	return nil
}

// userMFAResetLocal is the break-glass form: it acts on the database directly, like
// `backd bootstrap`, so an operator can recover the only administrator of a realm.
func userMFAResetLocal(c *kli.CommandContext) error {
	if !flag(c, "yes") {
		return errors.New("resetting a second factor ends all of the user's sessions and can't be undone: add --yes to confirm")
	}
	ctx := context.Background()
	uio := ioOf(c)
	a, err := setup(ctx, c.Getenv, uio.stderr)
	if err != nil {
		return err
	}
	defer a.close()
	users, err := a.users(ctx, str(c, "realm"))
	if err != nil {
		return err
	}
	u, err := users.Find(ctx, str(c, "email"))
	if err != nil {
		return fmt.Errorf("user not found: %s", str(c, "email"))
	}
	ctx = auth.WithAuditSource(ctx, func() auth.AuditSource { return auth.AuditSource{Actor: auth.ActorInstance} })
	if err := users.ResetMFA(ctx, u.ID, ""); err != nil {
		return mfaResetError(u.Email, err)
	}
	fmt.Fprintf(uio.stdout, "second factor of %s removed; their sessions ended (audited as %s)\n", u.Email, auth.ActorInstance)
	return nil
}

func mfaResetError(email string, err error) error {
	if errors.Is(err, auth.ErrMFANotEnabled) {
		return fmt.Errorf("%s has no second factor", email)
	}
	return err
}

// userExport handles `backd user export`: it asks for the export of a user's personal data, waits
// for it, and writes the package (a zip) to --out. The user is told, unless --no-notify and a
// reason say why not.
func userExport(c *userCtx) error {
	body := map[string]any{}
	if v := str(c.cmd, "ticket"); v != "" {
		body["ticket"] = v
	}
	if flag(c.cmd, "no-notify") {
		if strings.TrimSpace(str(c.cmd, "reason")) == "" {
			return errors.New("--no-notify needs --reason: say why the user is not told, it is kept in the audit record")
		}
		body["no_notify"] = true
		body["reason"] = str(c.cmd, "reason")
	} else if str(c.cmd, "reason") != "" {
		return errors.New("--reason only goes with --no-notify: it is why the user is not told")
	}
	wait := 30 * time.Minute
	if v := str(c.cmd, "timeout"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("--timeout %q: use a duration such as 10m", v)
		}
		wait = d
	}
	u, err := c.find()
	if err != nil {
		return err
	}
	proof, err := c.t.proof(c.uio)
	if err != nil {
		return err
	}
	base := "_admin/users/" + url.PathEscape(u.ID) + "/export"
	var rec struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Step      string `json:"step"`
		Error     string `json:"error"`
		Silent    bool   `json:"silent"`
		Notified  *bool  `json:"notified"`
		NotifyErr string `json:"notify_error"`
		ExpiresAt string `json:"expires_at"`
		Size      int64  `json:"size"`
	}
	if _, err := c.t.callHeaders("POST", base, nil, body, &rec, proof); err != nil {
		return err
	}
	switch {
	case rec.Silent:
		fmt.Fprintf(c.uio.stderr, "export %s of %s started; the user is not told (audited with your reason)\n", rec.ID, u.Email)
	case rec.Notified != nil && !*rec.Notified:
		fmt.Fprintf(c.uio.stderr, "export %s of %s started; the user could NOT be told: %s\n", rec.ID, u.Email, rec.NotifyErr)
	default:
		fmt.Fprintf(c.uio.stderr, "export %s of %s started; the user is told by email\n", rec.ID, u.Email)
	}
	if flag(c.cmd, "no-wait") {
		fmt.Fprintln(c.uio.stdout, rec.ID)
		return nil
	}

	deadline := time.Now().Add(wait)
	last := ""
	for rec.Status != "ready" {
		if rec.Status == "failed" || rec.Status == "expired" {
			return fmt.Errorf("the export %s: %s", rec.Status, rec.Error)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the export is still %s after %s; it goes on in the background: fetch it later with `backd user export-download` (id %s)", rec.Status, wait, rec.ID)
		}
		time.Sleep(exportPoll)
		if err := c.t.call("GET", base+"s/"+url.PathEscape(rec.ID), nil, nil, &rec); err != nil {
			return err
		}
		if rec.Step != "" && rec.Step != last {
			last = rec.Step
			fmt.Fprintln(c.uio.stderr, "  "+last)
		}
	}
	out := str(c.cmd, "out")
	if out == "" {
		out = "export-" + rec.ID + ".zip"
	}
	return downloadExport(c, base+"s/"+url.PathEscape(rec.ID)+"/download", out, proof, rec.ExpiresAt)
}

// exportPoll is how often the CLI asks whether an export is ready.
var exportPoll = 2 * time.Second

// userExportDownload handles `backd user export-download`: it fetches an export made earlier.
func userExportDownload(c *userCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	proof, err := c.t.proof(c.uio)
	if err != nil {
		return err
	}
	id := str(c.cmd, "export")
	out := str(c.cmd, "out")
	if out == "" {
		out = "export-" + id + ".zip"
	}
	return downloadExport(c, "_admin/users/"+url.PathEscape(u.ID)+"/exports/"+url.PathEscape(id)+"/download", out, proof, "")
}

// downloadExport writes a package to path, never over a file that is there.
func downloadExport(c *userCtx, apiPath, path string, proof map[string]string, expires string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("can't write %s (an existing file is never overwritten): %w", path, err)
	}
	res, err := c.t.open(apiPath, nil, proof)
	if err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	defer res.Body.Close()
	n, err := io.Copy(f, res.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("download: %w", err)
	}
	msg := fmt.Sprintf("wrote %s (%d bytes): personal data, keep it as carefully as the database", path, n)
	if expires != "" {
		msg += "; the copy kept by the server is deleted on " + expires
	}
	fmt.Fprintln(c.uio.stdout, msg)
	return nil
}
