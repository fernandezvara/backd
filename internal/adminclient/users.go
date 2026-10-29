package adminclient

import (
	"context"
	"net/url"
)

// Users are the realm's users (_admin/users).
type Users struct{ base }

// List lists users (filter with q: email, limit, after, …).
func (x *Users) List(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("users"), q, nil, out, opts)
	return err
}

func (x *Users) Create(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users"), nil, body, out, opts)
	return err
}

// Patch changes a user (disabling, roles, …).
func (x *Users) Patch(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "PATCH", s("users", id), nil, body, out, opts)
	return err
}

// Erase erases a user and their data; the answer is the job doing it.
func (x *Users) Erase(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("users", id), nil, nil, out, opts)
	return err
}

func (x *Users) SetPassword(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users", id, "password"), nil, body, out, opts)
	return err
}

func (x *Users) ExpirePassword(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users", id, "expire-password"), nil, body, out, opts)
	return err
}

func (x *Users) ChangeEmail(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users", id, "email"), nil, body, out, opts)
	return err
}

// Role gives a role to a user, or takes it away.
func (x *Users) Role(ctx context.Context, id, role string, grant bool, out any, opts ...Opt) error {
	method := "DELETE"
	if grant {
		method = "PUT"
	}
	_, err := x.call(ctx, method, s("users", id, "roles", role), nil, nil, out, opts)
	return err
}

// Owned reports what a user owns.
func (x *Users) Owned(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("users", id, "owned"), nil, nil, out, opts)
	return err
}

// SetNetworks sets the networks a user may sign in or administer from.
func (x *Users) SetNetworks(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "PUT", s("users", id, "networks"), nil, body, out, opts)
	return err
}

func (x *Users) Identities(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("users", id, "identities"), nil, nil, out, opts)
	return err
}

func (x *Users) UnlinkIdentity(ctx context.Context, id, provider string, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("users", id, "identities", provider), nil, nil, nil, opts)
	return err
}

func (x *Users) MFA(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("users", id, "mfa"), nil, nil, out, opts)
	return err
}

func (x *Users) ResetMFA(ctx context.Context, id string, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users", id, "mfa", "reset"), nil, nil, nil, opts)
	return err
}

func (x *Users) ResetLink(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users", id, "reset-link"), nil, body, out, opts)
	return err
}

// AcceptConsents records consents on behalf of a user.
func (x *Users) AcceptConsents(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users", id, "consents"), nil, body, out, opts)
	return err
}

func (x *Users) Devices(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("users", id, "devices"), nil, nil, out, opts)
	return err
}

func (x *Users) DeleteDevice(ctx context.Context, id, device string, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("users", id, "devices", device), nil, nil, nil, opts)
	return err
}

// StartExport starts a copy of a user's data.
func (x *Users) StartExport(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("users", id, "export"), nil, body, out, opts)
	return err
}

// Export reads one of a user's exports.
func (x *Users) Export(ctx context.Context, id, export string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("users", id, "exports", export), nil, nil, out, opts)
	return err
}

// OpenExport opens the package of a ready export: the caller reads and closes the body.
func (x *Users) OpenExport(ctx context.Context, id, export string, opts ...Opt) (*Stream, error) {
	return x.stream(ctx, s("users", id, "exports", export, "download"), nil, opts)
}

// Orgs are the realm's organizations (_admin/orgs).
type Orgs struct{ base }

func (x *Orgs) List(ctx context.Context, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("orgs"), q, nil, out, opts)
	return err
}

func (x *Orgs) Get(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("orgs", id), nil, nil, out, opts)
	return err
}

func (x *Orgs) Create(ctx context.Context, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("orgs"), nil, body, out, opts)
	return err
}

func (x *Orgs) Patch(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "PATCH", s("orgs", id), nil, body, out, opts)
	return err
}

func (x *Orgs) Delete(ctx context.Context, id string, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("orgs", id), nil, nil, nil, opts)
	return err
}

func (x *Orgs) Members(ctx context.Context, id string, q url.Values, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("orgs", id, "members"), q, nil, out, opts)
	return err
}

func (x *Orgs) AddMember(ctx context.Context, id string, body any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("orgs", id, "members"), nil, body, nil, opts)
	return err
}

func (x *Orgs) SetOwner(ctx context.Context, id string, body any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("orgs", id, "owner"), nil, body, nil, opts)
	return err
}

func (x *Orgs) SetClaims(ctx context.Context, id string, claims any, opts ...Opt) error {
	_, err := x.call(ctx, "PUT", s("orgs", id, "claims"), nil, claims, nil, opts)
	return err
}

func (x *Orgs) SetSlug(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "PUT", s("orgs", id, "slug"), nil, body, out, opts)
	return err
}

func (x *Orgs) Reactivate(ctx context.Context, id string, body any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("orgs", id, "reactivate"), nil, body, nil, opts)
	return err
}

// Owned reports what an organization owns.
func (x *Orgs) Owned(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("orgs", id, "owned"), nil, nil, out, opts)
	return err
}

func (x *Orgs) Invite(ctx context.Context, id string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "POST", s("orgs", id, "invitations"), nil, body, out, opts)
	return err
}

func (x *Orgs) Invitations(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("orgs", id, "invitations"), nil, nil, out, opts)
	return err
}

func (x *Orgs) RevokeInvitation(ctx context.Context, id, invitation string, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("orgs", id, "invitations", invitation), nil, nil, nil, opts)
	return err
}

// SSOList lists an organization's sign-in connections.
func (x *Orgs) SSOList(ctx context.Context, id string, out any, opts ...Opt) error {
	_, err := x.call(ctx, "GET", s("orgs", id, "sso"), nil, nil, out, opts)
	return err
}

func (x *Orgs) SSOSet(ctx context.Context, id, provider string, body, out any, opts ...Opt) error {
	_, err := x.call(ctx, "PUT", s("orgs", id, "sso", provider), nil, body, out, opts)
	return err
}

func (x *Orgs) SSODelete(ctx context.Context, id, provider string, opts ...Opt) error {
	_, err := x.call(ctx, "DELETE", s("orgs", id, "sso", provider), nil, nil, nil, opts)
	return err
}
