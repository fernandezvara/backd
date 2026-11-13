package account

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/pkg/client"
)

func LoginAction(c *kli.CommandContext) error {
	return login(cmdutil.Str(c, "realm"), cmdutil.Str(c, "url"), cmdutil.Str(c, "email"), cmdutil.Flag(c, "magic-link"), cmdutil.Str(c, "strategy"), c.Getenv, cmdutil.IOOf(c))
}

func LogoutAction(c *kli.CommandContext) error {
	return logout(cmdutil.Str(c, "realm"), cmdutil.Str(c, "url"), c.Getenv, cmdutil.IOOf(c))
}

func WhoamiAction(c *kli.CommandContext) error {
	return whoami(cmdutil.Str(c, "realm"), cmdutil.Str(c, "url"), c.Getenv, cmdutil.IOOf(c))
}

// sessionAnswer is the session a sign-in ends in.
type sessionAnswer struct {
	Token     string
	ExpiresAt string
	Email     string
}

func sessionOf(s *client.Session) sessionAnswer {
	a := sessionAnswer{Token: s.Token, ExpiresAt: s.ExpiresAt}
	if s.User != nil {
		a.Email = s.User.Email
	}
	return a
}

func login(realm, flagURL, email string, magicLink bool, strategy string, getenv func(string) string, uio cmdutil.IO) error {
	creds, path, err := clicore.LoadCredentials(getenv)
	if err != nil {
		return err
	}
	u, err := clicore.ResolveURL(flagURL, getenv, creds)
	if err != nil {
		return err
	}
	hc, err := clicore.HTTPClient(getenv)
	if err != nil {
		return err
	}
	if PlainRemote(u) {
		fmt.Fprintf(uio.Stderr, "warning: %s isn't HTTPS: the password and session travel unencrypted\n", u)
	}
	terminal := cmdutil.IsTerminal(uio.Stdin)
	if email == "" {
		if !terminal {
			return errors.New("--email is required when standard input isn't a terminal")
		}
		fmt.Fprint(uio.Stderr, "Email: ")
		line, err := bufio.NewReader(uio.Stdin).ReadString('\n')
		if err != nil {
			return errors.New("no email given")
		}
		email = strings.TrimSpace(line)
	}
	if !cmdutil.IsTerminal(uio.Stdin) {
		// The password and a code are two lines of the same input.
		uio.Stdin = bufio.NewReader(uio.Stdin)
	}
	t, err := clicore.MakeTarget(u, realm, "", false, hc)
	if err != nil {
		return err
	}
	var res sessionAnswer
	if magicLink {
		if err := magicLinkSignIn(t, uio, email, strategy, &res); err != nil {
			return err
		}
	} else {
		if strategy, err = chooseStrategy(t, uio, strategy, terminal); err != nil {
			return err
		}
		if err := refuseBrowserStrategy(t, strategy); err != nil {
			return err
		}
		pw, err := cmdutil.ReadSecret(uio, false, "Password")
		if err != nil {
			return err
		}
		step, err := t.Client.Auth().SignIn(t.Ctx, client.SignInInput{Email: email, Password: pw, Strategy: strategy})
		if err != nil {
			return err
		}
		if err := finishStep(t, uio, step, &res); err != nil {
			return err
		}
	}
	if res.Token == "" {
		return errors.New("the server gave no session")
	}
	if creds.Sessions[u] == nil {
		creds.Sessions[u] = map[string]clicore.StoredAuth{}
	}
	creds.Sessions[u][realm] = clicore.StoredAuth{Token: res.Token, Email: res.Email, ExpiresAt: res.ExpiresAt}
	creds.DefaultURL = u
	if err := creds.Save(path); err != nil {
		return fmt.Errorf("logged in, but can't store the session: %w", err)
	}
	fmt.Fprintf(uio.Stdout, "logged in to realm %s on %s as %s (session expires at %s at the latest)\n", realm, u, res.Email, res.ExpiresAt)
	if getenv("BACKD_API_KEY") != "" {
		fmt.Fprintln(uio.Stderr, "note: BACKD_API_KEY is set, so commands use that key instead of this session")
	}
	return nil
}

func logout(realm, flagURL string, getenv func(string) string, uio cmdutil.IO) error {
	t, err := clicore.NewTarget(flagURL, realm, getenv, true)
	if err != nil {
		return err
	}
	// A session the server no longer accepts is forgotten anyway.
	var ae *clicore.APIError
	if err := t.Client.Auth().Logout(t.Ctx); err != nil && (!errors.As(err, &ae) || ae.Status != 401) {
		return err
	}
	creds, path, err := clicore.LoadCredentials(getenv)
	if err != nil {
		return err
	}
	delete(creds.Sessions[t.URL], realm)
	if len(creds.Sessions[t.URL]) == 0 {
		delete(creds.Sessions, t.URL)
	}
	if err := creds.Save(path); err != nil {
		return err
	}
	fmt.Fprintf(uio.Stdout, "logged out of realm %s on %s\n", realm, t.URL)
	return nil
}

func whoami(realm, flagURL string, getenv func(string) string, uio cmdutil.IO) error {
	t, err := clicore.NewTarget(flagURL, realm, getenv, true)
	if err != nil {
		return err
	}
	me, err := t.Client.Auth().Me(t.Ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(uio.Stdout, "%s (id %s) on realm %s at %s; roles: %s\n", me.Email, me.ID, realm, t.URL, orNone(me.Roles))
	if getenv("BACKD_API_KEY") != "" {
		fmt.Fprintln(uio.Stderr, "note: BACKD_API_KEY is set, so commands use that key instead of this session")
	}
	return nil
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ",")
}

// plainRemote reports whether u is plain HTTP to a host other than the
// loopback, localhost or an unqualified name (a container on a private
// network, such as http://backd:8080).
func PlainRemote(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// chooseStrategy is the strategy to sign in with the password: the one named; else the only strategy
// that starts with the password; with several it asks on a terminal and names them otherwise. A realm
// whose strategies cannot be listed is left to the server (which answers strategy_required if needed).
func chooseStrategy(t *clicore.Target, uio cmdutil.IO, named string, terminal bool) (string, error) {
	if named != "" {
		return named, nil
	}
	list, err := t.Client.Auth().Strategies(t.Ctx)
	if err != nil {
		return "", nil
	}
	var names []string
	for _, st := range list {
		if len(st.Methods) > 0 && st.Methods[0] == "password" {
			names = append(names, st.Name)
		}
	}
	switch {
	case len(names) <= 1:
		return "", nil
	case !terminal:
		return "", fmt.Errorf("this realm has several strategies that start with the password (%s): name one with --strategy", strings.Join(names, ", "))
	}
	fmt.Fprintln(uio.Stderr, "Strategies:")
	for i, n := range names {
		fmt.Fprintf(uio.Stderr, "  %d. %s\n", i+1, n)
	}
	for range 3 {
		answer, err := cmdutil.ReadLine(uio, "Strategy (number or name)")
		if err != nil {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if i, err := strconv.Atoi(answer); err == nil && i >= 1 && i <= len(names) {
			return names[i-1], nil
		}
		if slices.Contains(names, answer) {
			return answer, nil
		}
		fmt.Fprintln(uio.Stderr, "that is not one of them")
	}
	return "", errors.New("no strategy chosen")
}

// StrategiesAction lists the realm's strategies and their methods; it needs no session.
func StrategiesAction(c *kli.CommandContext) error {
	creds, _, err := clicore.LoadCredentials(c.Getenv)
	if err != nil {
		return err
	}
	u, err := clicore.ResolveURL(cmdutil.Str(c, "url"), c.Getenv, creds)
	if err != nil {
		return err
	}
	hc, err := clicore.HTTPClient(c.Getenv)
	if err != nil {
		return err
	}
	t, err := clicore.MakeTarget(u, cmdutil.Str(c, "realm"), "", false, hc)
	if err != nil {
		return err
	}
	list, err := t.Client.Auth().Strategies(t.Ctx)
	if err != nil {
		return err
	}
	out := cmdutil.IOOf(c).Stdout
	for _, st := range list {
		fmt.Fprintf(out, "%s: %s\n", st.Name, strings.Join(st.Methods, " -> "))
	}
	return nil
}

// errNeedsBrowser is what a method that only a browser or an app can do answers in the terminal.
// A passkey is signed by an authenticator the browser talks to, and a browser hand-off would be a
// phishing pattern (design/mfa-passwordless.md §16), so there is no passkey sign-in here.
var errNeedsBrowser = errors.New("this method needs a browser or an app: a passkey cannot be used from the terminal (sign in with a strategy that uses a password, a code or a link)")

// refuseBrowserStrategy stops before the password is asked when the named strategy starts with a passkey.
func refuseBrowserStrategy(t *clicore.Target, name string) error {
	if name == "" {
		return nil
	}
	list, err := t.Client.Auth().Strategies(t.Ctx)
	if err != nil {
		return nil // the sign-in itself will say what is wrong
	}
	for _, st := range list {
		if st.Name == name && len(st.Methods) > 0 && st.Methods[0] == "passkey" {
			return errNeedsBrowser
		}
	}
	return nil
}
