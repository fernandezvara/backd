package admin

import (
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

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/adminclient"
	"github.com/fernandezvara/backd/internal/cmdutil"
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
type UserCtx struct {
	Target *clicore.Target
	Cmd    *kli.CommandContext
	Email  string
	IO     cmdutil.IO
	Local  *registry.RealmSettings // the realm's settings, when CONFIG_DIR has it
}

// find looks a user up by email.
func (c *UserCtx) find() (adminUser, error) {
	var page struct{ Items []adminUser }
	if err := c.Target.Adm.Users.List(c.Target.Ctx, url.Values{"email": {c.Email}}, &page); err != nil {
		return adminUser{}, err
	}
	if len(page.Items) == 0 {
		return adminUser{}, fmt.Errorf("user not found: %s", c.Email)
	}
	return page.Items[0], nil
}

// onUser finds the user and sends a request to one of their resources.
func (c *UserCtx) onUser(do func(id string, out *adminUser) error) (adminUser, error) {
	u, err := c.find()
	if err != nil {
		return u, err
	}
	var out adminUser
	err = do(u.ID, &out)
	if out.ID == "" {
		out = u
	}
	return out, err
}

func UserCreate(c *UserCtx) error {
	body := map[string]any{"email": c.Email}
	noPassword := cmdutil.Flag(c.Cmd, "no-password")
	if !noPassword {
		p, err := cmdutil.ReadSecret(c.IO, true, "Password")
		if err != nil {
			return err
		}
		body["password"] = p
	}
	var u adminUser
	if err := c.Target.Adm.Users.Create(c.Target.Ctx, body, &u); err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "created user %s (id %s)\n", u.Email, u.ID)
	if noPassword {
		fmt.Fprintln(c.IO.Stdout, "the user has no password yet; set one with `backd user set-password`")
	}
	return nil
}

func UserList(c *UserCtx) error {
	tw := tabwriter.NewWriter(c.IO.Stdout, 0, 0, 2, ' ', 0)
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
		if err := c.Target.Adm.Users.List(c.Target.Ctx, q, &page); err != nil {
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

func UserSetPassword(c *UserCtx) error {
	p, err := cmdutil.ReadSecret(c.IO, true, "Password")
	if err != nil {
		return err
	}
	u, err := c.onUser(func(id string, out *adminUser) error {
		return c.Target.Adm.Users.SetPassword(c.Target.Ctx, id, map[string]string{"password": p}, out)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "password set for %s; all of their sessions were revoked\n", u.Email)
	return nil
}

func UserExpirePassword(c *UserCtx) error {
	body := map[string]string{}
	if r := cmdutil.Str(c.Cmd, "reason"); r != "" {
		body["reason"] = r
	}
	u, err := c.onUser(func(id string, out *adminUser) error {
		return c.Target.Adm.Users.ExpirePassword(c.Target.Ctx, id, body, out)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "the password of %s expired; they can sign in again after a password reset or a new password from an administrator (their sessions were kept)\n", u.Email)
	return nil
}

func UserExpirePasswords(c *UserCtx) error {
	if !cmdutil.Flag(c.Cmd, "yes") {
		msg := "this expires the password of every account in the realm and ends every session"
		if cmdutil.Flag(c.Cmd, "keep-sessions") {
			msg = "this expires the password of every account in the realm"
		}
		return errors.New(msg + ": add --yes to confirm")
	}
	body := map[string]any{"keep_sessions": cmdutil.Flag(c.Cmd, "keep-sessions"), "notify": cmdutil.Flag(c.Cmd, "notify")}
	if r := cmdutil.Str(c.Cmd, "reason"); r != "" {
		body["reason"] = r
	}
	var out struct {
		ValidAfter string `json:"valid_after"`
		Accounts   int64
		Sessions   int64
		NotifyJob  string `json:"notify_job"`
	}
	proof, err := c.Target.Proof(c.IO)
	if err != nil {
		return err
	}
	if err := c.Target.Adm.Passwords.Expire(c.Target.Ctx, body, &out, clicore.WithProof(proof)...); err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "every password set before %s expired: %d accounts with a password, %d sessions ended\nusers choose a new password with a reset (a sign-in with the old one queues the email), or you set one with `backd user set-password`\n", out.ValidAfter, out.Accounts, out.Sessions)
	if out.NotifyJob != "" {
		fmt.Fprintf(c.IO.Stdout, "telling the users by email: job %s (follow it with `backd functions jobs`; `backd user notify-expired` starts it again if it is cancelled or fails)\n", out.NotifyJob)
	}
	return nil
}

func UserNotifyExpired(c *UserCtx) error {
	var out struct {
		ID string `json:"id"`
	}
	proof, err := c.Target.Proof(c.IO)
	if err != nil {
		return err
	}
	if err := c.Target.Adm.Passwords.Notify(c.Target.Ctx, &out, clicore.WithProof(proof)...); err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "telling the users whose password expired: job %s; users already told are skipped (follow it with `backd functions jobs`)\n", out.ID)
	return nil
}

func UserRevokeSessions(c *UserCtx) error {
	if !cmdutil.Flag(c.Cmd, "all") {
		return cmdutil.UsageErr(errors.New("--all is required: it ends the sessions of every user (to end one user's, set a password or disable them)"))
	}
	if !cmdutil.Flag(c.Cmd, "yes") {
		return errors.New("this ends every session in the realm: add --yes to confirm")
	}
	var out struct{ Sessions int64 }
	proof, err := c.Target.Proof(c.IO)
	if err != nil {
		return err
	}
	if err := c.Target.Adm.Sessions.RevokeAll(c.Target.Ctx, &out, clicore.WithProof(proof)...); err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "%d sessions ended\n", out.Sessions)
	return nil
}

func UserResetLink(c *UserCtx) error {
	body := map[string]string{}
	if r := cmdutil.Str(c.Cmd, "redirect-to"); r != "" {
		body["redirect_to"] = r
	}
	u, err := c.find()
	if err != nil {
		return err
	}
	proof, err := c.Target.Proof(c.IO)
	if err != nil {
		return err
	}
	var out struct {
		Link      string
		ExpiresAt string `json:"expires_at"`
	}
	if err := c.Target.Adm.Users.ResetLink(c.Target.Ctx, u.ID, body, &out, clicore.WithProof(proof)...); err != nil {
		return err
	}
	fmt.Fprintln(c.IO.Stdout, out.Link)
	fmt.Fprintf(c.IO.Stderr, "reset link for %s, shown once; it works once and expires at %s. Hand it over by a channel you trust: whoever holds it can set the password. Nothing was sent.\n", u.Email, out.ExpiresAt)
	return nil
}

func UserChangeEmail(c *UserCtx) error {
	u, err := c.onUser(func(id string, out *adminUser) error {
		return c.Target.Adm.Users.ChangeEmail(c.Target.Ctx, id, map[string]string{"email": cmdutil.Str(c.Cmd, "new-email")}, out)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "email changed to %s; all of their sessions were revoked, and both addresses were told (the old one can undo it for 7 days)\n", u.Email)
	return nil
}

// userPatch returns a command that changes one field of the user and
// prints message with the user's email.
func UserPatch(body map[string]bool, message string) func(*UserCtx) error {
	return func(c *UserCtx) error {
		u, err := c.onUser(func(id string, out *adminUser) error { return c.Target.Adm.Users.Patch(c.Target.Ctx, id, body, out) })
		if err != nil {
			return err
		}
		fmt.Fprintf(c.IO.Stdout, message, u.Email)
		return nil
	}
}

func UserDelete(c *UserCtx) error {
	if !cmdutil.Flag(c.Cmd, "yes") {
		return errors.New("deleting a user can't be undone; add --yes to confirm")
	}
	wait, err := time.ParseDuration(cmdutil.Str(c.Cmd, "wait"))
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
	if err := c.Target.Adm.Users.Erase(c.Target.Ctx, u.ID, &job); err != nil {
		return err
	}
	out := c.IO.Stdout
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
		if err := c.Target.Adm.Jobs.List(c.Target.Ctx, url.Values{"origin": {"backd:account.erase"}, "limit": {"100"}}, &jobs); err != nil {
			return err
		}
		for _, j := range jobs.Items {
			if j.ID == job.ID && j.Status == "done" {
				return printErased(c, u.ID)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Fprintf(out, "still running after %s: a worker (backd-server worker, or serve --with-worker) must be up to finish it; check `backd audit --action user.erased`\n", wait)
	return nil
}

// printErased prints the counts of a finished erase from its audit record.
func printErased(c *UserCtx, id string) error {
	var page struct {
		Items []struct {
			Details struct {
				Counts         map[string]int64 `json:"counts"`
				NeedsAttention bool             `json:"needs_attention"`
				Error          string           `json:"error"`
			} `json:"details"`
		} `json:"items"`
	}
	if err := c.Target.Adm.Audit.List(c.Target.Ctx, url.Values{"action": {"user.erased"}, "target": {"user:" + id}, "limit": {"1"}}, &page); err != nil {
		return err
	}
	out := c.IO.Stdout
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
func UserOwned(c *UserCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	var report ownedReport
	if err := c.Target.Adm.Users.Owned(c.Target.Ctx, u.ID, &report); err != nil {
		return err
	}
	out := c.IO.Stdout
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
func LocalSettings(realm string, getenv func(string) string) *registry.RealmSettings {
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

func UserRole(c *UserCtx, verb, role string) error {
	u, err := c.onUser(func(id string, out *adminUser) error {
		return c.Target.Adm.Users.Role(c.Target.Ctx, id, role, verb != "remove", out)
	})
	if err != nil {
		return err
	}
	realm := c.Target.Realm
	if verb == "add" {
		fmt.Fprintf(c.IO.Stdout, "role %q assigned to %s\n", role, u.Email)
	} else {
		fmt.Fprintf(c.IO.Stdout, "role %q removed from %s\n", role, u.Email)
	}
	switch {
	case c.Local == nil:
		fmt.Fprintf(c.IO.Stderr, "note: role assignments that %s/realm.yaml doesn't list under roles.%s.users live only in the database, and those it lists come back at the next start.\n", realm, role)
	case verb == "add" && !slices.Contains(c.Local.Roles[role].Users, u.Email):
		fmt.Fprintf(c.IO.Stderr, "warning: this assignment is stored only in the database. If the realm is rebuilt from its config, it won't come back.\n"+
			"To keep it, list %s under roles.%s.users in %s/realm.yaml.\n", u.Email, role, realm)
	case verb == "remove" && slices.Contains(c.Local.Roles[role].Users, u.Email):
		fmt.Fprintf(c.IO.Stderr, "warning: %s/realm.yaml assigns this role to %s, so it will be assigned again at the next `backd-server serve` or `backd-server provision`.\n"+
			"Remove %s from roles.%s.users in realm.yaml too.\n", realm, u.Email, u.Email, role)
	}
	return nil
}

// userNetworks shows or changes a user's network restrictions.
func UserNetworks(c *UserCtx) error {
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
		v := cmdutil.Str(c.Cmd, n.flag)
		if v == "" {
			continue
		}
		changed = true
		*n.list = []string{}
		if v != "none" {
			nets, err := registry.ParseNetworks(strings.Split(v, ","))
			if err != nil {
				return cmdutil.UsageErr(fmt.Errorf("--%s: want comma-separated IP addresses or CIDR networks, or none; got %q", n.flag, v))
			}
			*n.list = nets.Strings()
		}
	}
	if changed {
		body := map[string][]string{"admin_networks": nonNil(admin), "login_networks": nonNil(login)}
		if err := c.Target.Adm.Users.SetNetworks(c.Target.Ctx, u.ID, body, &u); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.IO.Stdout, "%s\n  admin networks: %s\n  login networks: %s\n", u.Email, orAny(u.AdminNetworks), orAny(u.LoginNetworks))
	if changed {
		if c.Local == nil {
			fmt.Fprintf(c.IO.Stderr, "note: if %s/realm.yaml sets networks for %s, they are restored at the next start.\n", c.Target.Realm, u.Email)
		} else if _, ok := c.Local.UserNetworks[u.Email]; ok {
			fmt.Fprintf(c.IO.Stderr, "warning: %s/realm.yaml sets networks for %s, so they will be restored at the next `backd-server serve` or `backd-server provision`. Change them in realm.yaml too.\n", c.Target.Realm, u.Email)
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

// userIdentities lists the user's sign-in methods.
func UserIdentities(c *UserCtx) error {
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
	if err := c.Target.Adm.Users.Identities(c.Target.Ctx, u.ID, &out); err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.IO.Stdout, 0, 4, 2, ' ', 0)
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
func UserUnlinkIdentity(c *UserCtx) error {
	provider := cmdutil.Str(c.Cmd, "provider")
	u, err := c.find()
	if err != nil {
		return err
	}
	if err := c.Target.Adm.Users.UnlinkIdentity(c.Target.Ctx, u.ID, provider); err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "%s can no longer sign in with %s\n", u.Email, provider)
	return nil
}

// userMFAStatus shows one user's second factor, or who must still set one up.
func UserMFAStatus(c *UserCtx) error {
	c.Email = cmdutil.Str(c.Cmd, "email")
	if c.Email != "" {
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
		if err := c.Target.Adm.Users.MFA(c.Target.Ctx, u.ID, &st); err != nil {
			return err
		}
		fmt.Fprintf(c.IO.Stdout, "%s: second factor %s; required by role: %t; recovery codes left: %d\n", u.Email, onOff(st.Enabled), st.Required, st.RecoveryCodesLeft)
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
	if err := c.Target.Adm.MFA.Status(c.Target.Ctx, &out); err != nil {
		return err
	}
	switch {
	case out.RequiredAll:
		fmt.Fprintln(c.IO.Stdout, "MFA is required for every user")
	case len(out.RequiredRole) > 0:
		fmt.Fprintf(c.IO.Stdout, "MFA is required for the roles: %s\n", strings.Join(out.RequiredRole, ", "))
	default:
		fmt.Fprintln(c.IO.Stdout, "MFA is required for no role (mfa.required_roles in realm.yaml)")
		return nil
	}
	if out.Total == 0 {
		fmt.Fprintln(c.IO.Stdout, "every user who must use it has set it up")
		return nil
	}
	fmt.Fprintf(c.IO.Stdout, "%d user(s) have none yet and will be asked at their next sign-in:\n", out.Total)
	for _, u := range out.Users {
		fmt.Fprintf(c.IO.Stdout, "  %s (%s)\n", u.Email, strings.Join(u.Roles, ", "))
	}
	if out.Total > len(out.Users) {
		fmt.Fprintf(c.IO.Stdout, "  … and %d more\n", out.Total-len(out.Users))
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
func UserMFAReset(c *UserCtx) error {
	if !cmdutil.Flag(c.Cmd, "yes") {
		return errors.New("resetting a second factor ends all of the user's sessions and can't be undone: add --yes to confirm")
	}
	u, err := c.find()
	if err != nil {
		return err
	}
	if err := c.Target.Adm.Users.ResetMFA(c.Target.Ctx, u.ID); err != nil {
		return err
	}
	fmt.Fprintf(c.IO.Stdout, "second factor of %s removed; their sessions ended\n", u.Email)
	return nil
}

// userExport handles `backd user export`: it asks for the export of a user's personal data, waits
// for it, and writes the package (a zip) to --out. The user is told, unless --no-notify and a
// reason say why not.
func UserExport(c *UserCtx) error {
	body := map[string]any{}
	if v := cmdutil.Str(c.Cmd, "ticket"); v != "" {
		body["ticket"] = v
	}
	if cmdutil.Flag(c.Cmd, "no-notify") {
		if strings.TrimSpace(cmdutil.Str(c.Cmd, "reason")) == "" {
			return errors.New("--no-notify needs --reason: say why the user is not told, it is kept in the audit record")
		}
		body["no_notify"] = true
		body["reason"] = cmdutil.Str(c.Cmd, "reason")
	} else if cmdutil.Str(c.Cmd, "reason") != "" {
		return errors.New("--reason only goes with --no-notify: it is why the user is not told")
	}
	wait := 30 * time.Minute
	if v := cmdutil.Str(c.Cmd, "timeout"); v != "" {
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
	proof, err := c.Target.Proof(c.IO)
	if err != nil {
		return err
	}
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
	if err := c.Target.Adm.Users.StartExport(c.Target.Ctx, u.ID, body, &rec, clicore.WithProof(proof)...); err != nil {
		return err
	}
	switch {
	case rec.Silent:
		fmt.Fprintf(c.IO.Stderr, "export %s of %s started; the user is not told (audited with your reason)\n", rec.ID, u.Email)
	case rec.Notified != nil && !*rec.Notified:
		fmt.Fprintf(c.IO.Stderr, "export %s of %s started; the user could NOT be told: %s\n", rec.ID, u.Email, rec.NotifyErr)
	default:
		fmt.Fprintf(c.IO.Stderr, "export %s of %s started; the user is told by email\n", rec.ID, u.Email)
	}
	if cmdutil.Flag(c.Cmd, "no-wait") {
		fmt.Fprintln(c.IO.Stdout, rec.ID)
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
		time.Sleep(ExportPoll)
		if err := c.Target.Adm.Users.Export(c.Target.Ctx, u.ID, rec.ID, &rec); err != nil {
			return err
		}
		if rec.Step != "" && rec.Step != last {
			last = rec.Step
			fmt.Fprintln(c.IO.Stderr, "  "+last)
		}
	}
	out := cmdutil.Str(c.Cmd, "out")
	if out == "" {
		out = "export-" + rec.ID + ".zip"
	}
	return downloadExport(c, func() (*adminclient.Stream, error) {
		return c.Target.AdmLong.Users.OpenExport(c.Target.Ctx, u.ID, rec.ID, clicore.WithProof(proof)...)
	}, out, rec.ExpiresAt)
}

// exportPoll is how often the CLI asks whether an export is ready.
var ExportPoll = 2 * time.Second

// userExportDownload handles `backd user export-download`: it fetches an export made earlier.
func UserExportDownload(c *UserCtx) error {
	u, err := c.find()
	if err != nil {
		return err
	}
	proof, err := c.Target.Proof(c.IO)
	if err != nil {
		return err
	}
	id := cmdutil.Str(c.Cmd, "export")
	out := cmdutil.Str(c.Cmd, "out")
	if out == "" {
		out = "export-" + id + ".zip"
	}
	return downloadExport(c, func() (*adminclient.Stream, error) {
		return c.Target.AdmLong.Users.OpenExport(c.Target.Ctx, u.ID, id, clicore.WithProof(proof)...)
	}, out, "")
}

// downloadExport writes a package to path, never over a file that is there.
func downloadExport(c *UserCtx, open func() (*adminclient.Stream, error), path, expires string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("can't write %s (an existing file is never overwritten): %w", path, err)
	}
	res, err := open()
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
	fmt.Fprintln(c.IO.Stdout, msg)
	return nil
}
