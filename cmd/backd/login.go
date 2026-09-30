package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/fernandezvara/cli"
	"golang.org/x/term"
)

func loginAction(c *cli.CommandContext) error {
	return login(str(c, "realm"), str(c, "url"), str(c, "email"), c.Getenv, ioOf(c))
}

func logoutAction(c *cli.CommandContext) error {
	return logout(str(c, "realm"), str(c, "url"), c.Getenv, ioOf(c))
}

func whoamiAction(c *cli.CommandContext) error {
	return whoami(str(c, "realm"), str(c, "url"), c.Getenv, ioOf(c))
}

type sessionAnswer struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	User      struct {
		Email string   `json:"email"`
		Roles []string `json:"roles"`
	} `json:"user"`
}

func login(realm, flagURL, email string, getenv func(string) string, uio userIO) error {
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
		fmt.Fprintf(uio.stderr, "warning: %s isn't HTTPS: the password and session travel unencrypted\n", u)
	}
	terminal := isTerminal(uio.stdin)
	if email == "" {
		if !terminal {
			return errors.New("--email is required when standard input isn't a terminal")
		}
		fmt.Fprint(uio.stderr, "Email: ")
		line, err := bufio.NewReader(uio.stdin).ReadString('\n')
		if err != nil {
			return errors.New("no email given")
		}
		email = strings.TrimSpace(line)
	}
	pw, err := readSecret(uio, false)
	if err != nil {
		return err
	}
	t := &target{url: u, realm: realm, http: hc}
	var res sessionAnswer
	if err := t.call("POST", "_auth/login", nil, map[string]string{"email": email, "password": pw}, &res); err != nil {
		return err
	}
	if creds.Sessions[u] == nil {
		creds.Sessions[u] = map[string]storedAuth{}
	}
	creds.Sessions[u][realm] = storedAuth{Token: res.Token, Email: res.User.Email, ExpiresAt: res.ExpiresAt}
	creds.DefaultURL = u
	if err := creds.save(path); err != nil {
		return fmt.Errorf("logged in, but can't store the session: %w", err)
	}
	fmt.Fprintf(uio.stdout, "logged in to realm %s on %s as %s (session expires at %s at the latest)\n", realm, u, res.User.Email, res.ExpiresAt)
	if getenv("BACKD_API_KEY") != "" {
		fmt.Fprintln(uio.stderr, "note: BACKD_API_KEY is set, so commands use that key instead of this session")
	}
	return nil
}

func logout(realm, flagURL string, getenv func(string) string, uio userIO) error {
	t, err := newTarget(flagURL, realm, getenv, true)
	if err != nil {
		return err
	}
	// A session the server no longer accepts is forgotten anyway.
	var ae *apiError
	if err := t.call("POST", "_auth/logout", nil, nil, nil); err != nil && !(errors.As(err, &ae) && ae.Status == 401) {
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
	fmt.Fprintf(uio.stdout, "logged out of realm %s on %s\n", realm, t.url)
	return nil
}

func whoami(realm, flagURL string, getenv func(string) string, uio userIO) error {
	t, err := newTarget(flagURL, realm, getenv, true)
	if err != nil {
		return err
	}
	var me struct {
		Email string   `json:"email"`
		ID    string   `json:"id"`
		Roles []string `json:"roles"`
	}
	if err := t.call("GET", "_auth/me", nil, nil, &me); err != nil {
		return err
	}
	fmt.Fprintf(uio.stdout, "%s (id %s) on realm %s at %s; roles: %s\n", me.Email, me.ID, realm, t.url, orNone(me.Roles))
	if getenv("BACKD_API_KEY") != "" {
		fmt.Fprintln(uio.stderr, "note: BACKD_API_KEY is set, so commands use that key instead of this session")
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

func isTerminal(r any) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
