package auth

import (
	"context"
	"errors"
	"fmt"
	"github.com/fernandezvara/backd/internal/registry"
	"maps"
	"slices"
)

// ErrUndeclaredRole means a role isn't declared in the realm's realm.yaml.
var ErrUndeclaredRole = errors.New("role is not declared in realm.yaml")

// seedRoles returns the roles realm.yaml assigns to email.
func (s *Users) seedRoles(email string) []string {
	var out []string
	for name, r := range s.Settings.Roles {
		if slices.Contains(r.Users, email) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// IsSeeded reports whether realm.yaml assigns role to email.
func (s *Users) IsSeeded(email, role string) bool {
	return slices.Contains(s.Settings.Roles[role].Users, email)
}

func (s *Users) checkRole(role string) error {
	if _, ok := s.Settings.Roles[role]; !ok {
		return fmt.Errorf("%w: %q", ErrUndeclaredRole, role)
	}
	return nil
}

// AddRole assigns a declared role to a user.
func (s *Users) AddRole(ctx context.Context, email, role string) error {
	if err := s.checkRole(role); err != nil {
		return err
	}
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	if err := s.Store.AddRoles(ctx, u.ID, []string{role}, s.now()); err != nil {
		return err
	}
	s.Audit(ctx, AuditRoleAdd, userTarget(u.ID), map[string]any{"role": role})
	return nil
}

// RemoveRole takes a role away from a user. An assignment seeded in
// realm.yaml comes back at the next seeding.
func (s *Users) RemoveRole(ctx context.Context, email, role string) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	if err := s.Store.RemoveRole(ctx, u.ID, role, s.now()); err != nil {
		return err
	}
	s.Audit(ctx, AuditRoleRemove, userTarget(u.ID), map[string]any{"role": role})
	return nil
}

// ApplyRoleSeeds adds the roles realm.yaml assigns to existing users. It
// never removes roles. Users who don't exist yet get theirs when created.
// It returns how many users were updated.
func (s *Users) ApplyRoleSeeds(ctx context.Context) (int, error) {
	byEmail := map[string][]string{}
	for name, r := range s.Settings.Roles {
		for _, e := range r.Users {
			byEmail[e] = append(byEmail[e], name)
		}
	}
	n := 0
	for _, email := range slices.Sorted(maps.Keys(byEmail)) {
		u, err := s.Store.UserByEmail(ctx, email)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return n, err
		}
		roles := byEmail[email]
		changed := false
		if missing := slices.DeleteFunc(slices.Clone(roles), func(r string) bool { return slices.Contains(u.Roles, r) }); len(missing) > 0 {
			if err := s.Store.AddRoles(ctx, u.ID, missing, s.now()); err != nil {
				return n, err
			}
			for _, r := range missing {
				s.AuditAs(ctx, ActorConfig, AuditRoleAdd, userTarget(u.ID), map[string]any{"role": r})
			}
			changed = true
		}
		// Network restrictions in realm.yaml are authoritative for the
		// emails they're set for.
		if un, ok := s.Settings.UserNetworks[email]; ok && (!un.Admin.Equal(u.AdminNetworks) || !un.Login.Equal(u.LoginNetworks)) {
			if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{AdminNetworks: &un.Admin, LoginNetworks: &un.Login}, s.now()); err != nil {
				return n, err
			}
			s.AuditAs(ctx, ActorConfig, AuditUserNetworks, userTarget(u.ID), networkDetails(un.Admin, un.Login))
			changed = true
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// RoleReport lists role assignments that realm.yaml doesn't account for.
type RoleReport struct {
	// DBOnly maps emails to roles assigned only in the database, which a
	// rebuild from realm.yaml wouldn't restore.
	DBOnly map[string][]string
	// Undeclared maps emails to roles realm.yaml no longer declares.
	Undeclared map[string][]string
	// NetworksDBOnly lists users with network restrictions that realm.yaml
	// doesn't set, which a rebuild from realm.yaml wouldn't restore.
	NetworksDBOnly []string
	// Admins counts the people who can use the admin API with a session:
	// emails seeded into an admin role in realm.yaml, and users holding
	// one in the database, each counted once.
	Admins int
}

// ReportRoles compares the stored assignments with realm.yaml.
func (s *Users) ReportRoles(ctx context.Context) (RoleReport, error) {
	rep := RoleReport{DBOnly: map[string][]string{}, Undeclared: map[string][]string{}}
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return rep, err
	}
	admins := map[string]bool{}
	for _, name := range s.Settings.AdminRoles() {
		for _, email := range s.Settings.Roles[name].Users {
			admins[email] = true
		}
	}
	for _, u := range users {
		if s.Settings.IsAdmin(u.Roles) {
			admins[u.Email] = true
		}
		if _, inConfig := s.Settings.UserNetworks[u.Email]; !inConfig && (len(u.AdminNetworks) > 0 || len(u.LoginNetworks) > 0) {
			rep.NetworksDBOnly = append(rep.NetworksDBOnly, u.Email)
		}
		for _, r := range u.Roles {
			switch {
			case s.checkRole(r) != nil:
				rep.Undeclared[u.Email] = append(rep.Undeclared[u.Email], r)
			case !s.IsSeeded(u.Email, r):
				rep.DBOnly[u.Email] = append(rep.DBOnly[u.Email], r)
			}
		}
	}
	rep.Admins = len(admins)
	return rep, nil
}

// ErrNetworksOutsideRealm means a user's admin networks aren't all inside
// the realm's admin.allowed_networks.
var ErrNetworksOutsideRealm = errors.New("admin networks must lie within the realm's admin.allowed_networks")

// SetNetworks replaces the user's network restrictions: admin limits their
// admin API requests (within the realm's allowed networks), login their
// login and session use. Empty lists remove the restriction. Settings
// realm.yaml doesn't hold live only in the database.
func (s *Users) SetNetworks(ctx context.Context, email string, admin, login registry.Networks) (User, error) {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return User{}, err
	}
	if !admin.Within(s.Settings.AdminNetworks) {
		return User{}, ErrNetworksOutsideRealm
	}
	if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{AdminNetworks: &admin, LoginNetworks: &login}, s.now()); err != nil {
		return User{}, err
	}
	s.Audit(ctx, AuditUserNetworks, userTarget(u.ID), networkDetails(admin, login))
	return s.Store.UserByID(ctx, u.ID)
}

func networkDetails(admin, login registry.Networks) map[string]any {
	return map[string]any{"admin_networks": admin.Strings(), "login_networks": login.Strings()}
}

// NetworksInConfig reports whether realm.yaml sets network restrictions
// for email; seeding would then overwrite changes made elsewhere.
func (s *Users) NetworksInConfig(email string) bool {
	_, ok := s.Settings.UserNetworks[email]
	return ok
}
