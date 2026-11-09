package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/pkg/client"
)

func loginAction(c *kli.CommandContext) error {
	return login(cmdutil.Str(c, "realm"), cmdutil.Str(c, "url"), cmdutil.Str(c, "email"), cmdutil.Flag(c, "magic-link"), c.Getenv, cmdutil.IOOf(c))
}

func logoutAction(c *kli.CommandContext) error {
	return logout(cmdutil.Str(c, "realm"), cmdutil.Str(c, "url"), c.Getenv, cmdutil.IOOf(c))
}

func whoamiAction(c *kli.CommandContext) error {
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

func login(realm, flagURL, email string, magicLink bool, getenv func(string) string, uio cmdutil.IO) error {
	creds, path, err := loadCredentials(getenv)
	if err != nil {
		return err
	}
	u, err := resolveURL(flagURL, getenv, creds)
	if err != nil {
		return err
	}
	hc, err := httpClient(getenv)
	if err != nil {
		return err
	}
	if plainRemote(u) {
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
	t, err := makeTarget(u, realm, "", false, hc)
	if err != nil {
		return err
	}
	var res sessionAnswer
	if magicLink {
		if err := magicLinkSignIn(t, uio, email, &res); err != nil {
			return err
		}
	} else {
		pw, err := cmdutil.ReadSecret(uio, false, "Password")
		if err != nil {
			return err
		}
		step, err := t.cl.Auth().SignIn(t.ctx, client.SignInInput{Email: email, Password: pw})
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
		creds.Sessions[u] = map[string]storedAuth{}
	}
	creds.Sessions[u][realm] = storedAuth{Token: res.Token, Email: res.Email, ExpiresAt: res.ExpiresAt}
	creds.DefaultURL = u
	if err := creds.save(path); err != nil {
		return fmt.Errorf("logged in, but can't store the session: %w", err)
	}
	fmt.Fprintf(uio.Stdout, "logged in to realm %s on %s as %s (session expires at %s at the latest)\n", realm, u, res.Email, res.ExpiresAt)
	if getenv("BACKD_API_KEY") != "" {
		fmt.Fprintln(uio.Stderr, "note: BACKD_API_KEY is set, so commands use that key instead of this session")
	}
	return nil
}

func logout(realm, flagURL string, getenv func(string) string, uio cmdutil.IO) error {
	t, err := newTarget(flagURL, realm, getenv, true)
	if err != nil {
		return err
	}
	// A session the server no longer accepts is forgotten anyway.
	var ae *apiError
	if err := t.cl.Auth().Logout(t.ctx); err != nil && (!errors.As(err, &ae) || ae.Status != 401) {
		return err
	}
	creds, path, err := loadCredentials(getenv)
	if err != nil {
		return err
	}
	delete(creds.Sessions[t.url], realm)
	if len(creds.Sessions[t.url]) == 0 {
		delete(creds.Sessions, t.url)
	}
	if err := creds.save(path); err != nil {
		return err
	}
	fmt.Fprintf(uio.Stdout, "logged out of realm %s on %s\n", realm, t.url)
	return nil
}

func whoami(realm, flagURL string, getenv func(string) string, uio cmdutil.IO) error {
	t, err := newTarget(flagURL, realm, getenv, true)
	if err != nil {
		return err
	}
	me, err := t.cl.Auth().Me(t.ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(uio.Stdout, "%s (id %s) on realm %s at %s; roles: %s\n", me.Email, me.ID, realm, t.url, orNone(me.Roles))
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
func plainRemote(u string) bool {
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
