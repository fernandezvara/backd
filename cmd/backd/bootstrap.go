package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fernandezvara/cli"

	"github.com/fernandezvara/backd/internal/auth"
)

// bootstrapAction handles `backd bootstrap`.
func bootstrapAction(c *cli.CommandContext) error {
	ctx := context.Background()
	uio := ioOf(c)
	a, err := setup(ctx, c.Getenv, uio.stderr)
	if err != nil {
		return err
	}
	defer a.close()
	realm := str(c, "realm")
	users, err := a.users(ctx, realm)
	if err != nil {
		return err
	}
	return bootstrap(ctx, users, realm, str(c, "email"), str(c, "role"), uio)
}

func bootstrap(ctx context.Context, users *auth.Users, realm, email, role string, uio userIO) error {
	roles := users.Settings.AdminRoles()
	switch {
	case len(roles) == 0:
		return fmt.Errorf("%s/realm.yaml declares no admin role: add one with `admin: true` under roles", realm)
	case role == "" && len(roles) > 1:
		return fmt.Errorf("%s/realm.yaml declares several admin roles (%s): pick one with --role", realm, strings.Join(roles, ", "))
	case role == "":
		role = roles[0]
	case !slices.Contains(roles, role):
		return fmt.Errorf("%q isn't an admin role of %s/realm.yaml; admin roles: %s", role, realm, strings.Join(roles, ", "))
	}
	existing, err := users.List(ctx)
	if err != nil {
		return err
	}
	for _, u := range existing {
		if users.Settings.IsAdmin(u.Roles) {
			return fmt.Errorf("realm %s already has an administrator (%s): log in as them with `backd login` and manage users through the admin API", realm, u.Email)
		}
	}
	if _, err := users.Find(ctx, email); err == nil {
		return fmt.Errorf("%s is already registered; bootstrap only creates new users. Give it an admin role in realm.yaml (roles.%s.users) and restart backd, or ask an administrator", email, role)
	} else if !errors.Is(err, auth.ErrNotFound) {
		return err
	}
	pw, err := readSecret(uio, true)
	if err != nil {
		return err
	}
	ctx = auth.WithAuditSource(ctx, func() auth.AuditSource { return auth.AuditSource{Actor: auth.ActorBootstrap} })
	u, err := users.Create(ctx, email, &pw)
	if err != nil {
		return err
	}
	if err := users.AddRole(ctx, u.Email, role); err != nil {
		return fmt.Errorf("created %s, but couldn't assign the role %q: %w", u.Email, role, err)
	}
	users.Audit(ctx, auth.AuditBootstrap, "user:"+u.ID, map[string]any{"role": role})
	fmt.Fprintf(uio.stdout, "created %s (id %s) with the admin role %q in realm %s\n", u.Email, u.ID, role, realm)
	if !users.IsSeeded(u.Email, role) {
		fmt.Fprintf(uio.stderr, "note: to keep this assignment if the realm is rebuilt from its config, list %s under roles.%s.users in %s/realm.yaml.\n", u.Email, role, realm)
	}
	fmt.Fprintf(uio.stderr, "next: `backd login --realm %s --url <server> --email %s`, then manage users and API keys with `backd user` and `backd apikey`\n", realm, u.Email)
	return nil
}
