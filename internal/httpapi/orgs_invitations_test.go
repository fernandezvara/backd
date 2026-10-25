package httpapi

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/executor"
)

func TestOrganizationInvitations(t *testing.T) {
	f := newRulesFixture(t)
	id := f.orgOf(t, f.ada, "Invite")
	base := orgsPath + "/" + id + "/invitations"

	// An invitation bound to an address: the answer holds the link, shown only once.
	rec, out := f.doH(t, "POST", base, `{"binding": "email", "email": "Bob@Example.com", "roles": ["editor"], "expires_in": "2d"}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	token, _ := out["token"].(string)
	link, _ := out["link"].(string)
	if !strings.HasPrefix(token, "bdo_") || link != "https://app.acme.example/join?token="+token+"&org="+id || out["status"] != "pending" {
		t.Errorf("invitation: %v", out)
	}
	// The list never shows tokens.
	rec, out = f.doH(t, "GET", base, "", bearer(f.ada))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), token) || len(out["items"].([]any)) != 1 {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	// The preview needs the token and nothing else; a token that doesn't work answers alike.
	for name, tok := range map[string]string{"nonsense": "nope", "unknown": "bdo_unknownunknownunknownunknownunknownunknown"} {
		rec, out := f.previewInvite(t, id, tok)
		if rec.Code != 400 || errCode(out) != "invalid_invitation" {
			t.Errorf("preview %s: %d %v", name, rec.Code, out)
		}
	}
	// Accepting is signing in with it: the invitation must be for the person.
	for name, c := range map[string]struct{ who string }{"someone else": {f.ada}, "unverified user": {f.carl}} {
		rec, out := f.acceptInvite(t, id, c.who, token)
		if rec.Code == 200 {
			t.Errorf("accept %s: %d %v", name, rec.Code, out)
		}
	}
	rec, out = f.previewInvite(t, id, token)
	if rec.Code != 200 || out["org"].(map[string]any)["name"] != "Invite" || out["roles"].([]any)[0] != "editor" {
		t.Errorf("preview: %d %v", rec.Code, out)
	}
	rec, out = f.acceptInvite(t, id, f.bob, token)
	if rec.Code != 200 || out["id"] != id {
		t.Fatalf("accept: %d %v", rec.Code, out)
	}
	if roles := out["roles"].([]any); len(roles) != 2 || roles[0] != "editor" {
		t.Errorf("roles after accepting: %v", roles)
	}
	// A bound invitation is used up by one acceptance.
	if rec, out := f.acceptInvite(t, id, f.bob, token); rec.Code != 401 || errCode(out) != "invalid_credentials" {
		t.Errorf("accept again: %d %v", rec.Code, out)
	}
	_, out = f.doH(t, "GET", base, "", bearer(f.ada))
	if st := out["items"].([]any)[0].(map[string]any)["status"]; st != "used" {
		t.Errorf("status = %v", st)
	}

	// Bound to a user: only that user.
	rec, out = f.doH(t, "POST", base, `{"binding": "user", "user": "`+f.bobID+`", "roles": ["member"]}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("user binding: %d %v", rec.Code, out)
	}
	userTok := out["token"].(string)
	if rec, out := f.acceptInvite(t, id, f.carl, userTok); rec.Code != 401 || errCode(out) != "invalid_credentials" {
		t.Errorf("another user: %d %v", rec.Code, out)
	}

	// An open link: uses, no roles that manage, and it works for anyone signed in.
	if rec, out := f.doH(t, "POST", base, `{"binding": "none", "roles": ["owner"]}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("open link with owner: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", base, `{"binding": "none", "max_uses": 2000}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("too many uses: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", base, `{"binding": "none", "expires_in": "90d"}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("too long: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", base, `{"binding": "none", "roles": ["editor"], "max_uses": 1}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("open: %d %v", rec.Code, out)
	}
	open := out["token"].(string)
	if rec, out := f.acceptInvite(t, id, f.carl, open); rec.Code != 200 {
		t.Errorf("carl joins by the open link: %d %v", rec.Code, out)
	}
	if rec, out := f.acceptInvite(t, id, f.ada, open); rec.Code != 401 {
		t.Errorf("a used-up open link: %d %v", rec.Code, out)
	}

	// Revoking ends it, and a new link replaces the old one.
	_, out = f.doH(t, "POST", base, `{"binding": "none"}`, jsonHdr(f.ada))
	oldTok, oldID := out["token"].(string), out["id"].(string)
	rec, out = f.doH(t, "POST", base+"/"+oldID+"/link", "", bearer(f.ada))
	newTok, _ := out["token"].(string)
	if rec.Code != 200 || newTok == "" || newTok == oldTok {
		t.Fatalf("new link: %d %v", rec.Code, out)
	}
	if rec, _ := f.previewInvite(t, id, oldTok); rec.Code != 400 {
		t.Errorf("the old link: %d", rec.Code)
	}
	if rec, _ := f.previewInvite(t, id, newTok); rec.Code != 200 {
		t.Errorf("the new link: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", base+"/"+oldID, "", bearer(f.ada)); rec.Code != 204 {
		t.Errorf("revoke: %d", rec.Code)
	}
	if rec, _ := f.previewInvite(t, id, newTok); rec.Code != 400 {
		t.Errorf("a revoked link: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", base+"/nosuch", "", bearer(f.ada)); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
	// Only managers of members invite, and only what they may give.
	if rec, _ := f.doH(t, "POST", base, `{"binding": "none"}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("an editor invites: %d", rec.Code)
	}
	if _, err := f.svc.AdminSetMemberRoles(context.Background(), id, f.bobID, []string{"admin"}); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "POST", base, `{"binding": "email", "email": "x@example.com", "roles": ["editor"]}`, jsonHdr(f.bob)); rec.Code != 201 {
		t.Errorf("an admin invites an editor: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", base, `{"binding": "email", "email": "y@example.com", "roles": ["owner"]}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("an admin invites an owner: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", orgsPath+"/nosuch/invitations", "", bearer(f.bob)); rec.Code != 403 {
		t.Errorf("unknown org: %d", rec.Code)
	}
	// Validation.
	for name, body := range map[string]string{
		"no binding": `{}`, "unknown binding": `{"binding": "phone"}`, "email without address": `{"binding": "email"}`,
		"user unknown": `{"binding": "user", "user": "nobody"}`, "send without email binding": `{"binding": "none", "send": true}`,
		"uses on a bound one": `{"binding": "email", "email": "a@example.com", "max_uses": 3}`, "unknown role": `{"binding": "none", "roles": ["wizard"]}`,
		"unknown team": `{"binding": "none", "teams": ["nosuch"]}`,
	} {
		if rec, out := f.doH(t, "POST", base, body, jsonHdr(f.ada)); rec.Code != 400 {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
}

// Teams of an invitation are given along with their roles: the caller must hold what they carry.
func TestOrganizationInvitationTeams(t *testing.T) {
	f := newRulesFixture(t)
	id := f.orgOf(t, f.ada, "Teams")
	store := f.svc.Store.(*authtest.MemStore)
	store.PutOrgTeam(auth.OrgTeam{ID: "team1", Org: id, Name: "Editors", Roles: []string{"editor"}})
	store.PutOrgTeam(auth.OrgTeam{ID: "team2", Org: id, Name: "Bosses", Roles: []string{"owner"}})
	base := orgsPath + "/" + id + "/invitations"
	rec, out := f.doH(t, "POST", base, `{"binding": "none", "teams": ["team1"]}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("with a team: %d %v", rec.Code, out)
	}
	tok := out["token"].(string)
	// A team that carries a managing role can't be on an open link.
	if rec, out := f.doH(t, "POST", base, `{"binding": "none", "teams": ["team2"]}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("team of owners on an open link: %d %v", rec.Code, out)
	}
	rec, out = f.acceptInvite(t, id, f.bob, tok)
	if rec.Code != 200 || len(out["teams"].([]any)) != 1 {
		t.Errorf("accept with a team: %d %v", rec.Code, out)
	}
}

// An invitation can be sent by email: the delivery function gets the link with the token the
// API answered, which backd keeps sealed until the invitation ends.
func TestOrganizationInvitationEmail(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	w := newTestWorker(t, f)
	w.reg = f.reg
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(`{"message_id":"pm-1"}`)}, nil
	})
	id := f.orgOf(t, f.ada, "Mail")
	base := orgsPath + "/" + id + "/invitations"
	rec, out := f.doH(t, "POST", base, `{"binding": "email", "email": "new@example.com", "roles": ["editor"], "send": true}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("send: %d %v", rec.Code, out)
	}
	token := out["token"].(string)
	if !w.RunOnce(context.Background()) {
		t.Fatal("no email job")
	}
	in := deliveredEmail(t, f.runner.last())
	if in.Kind != "org-invitation" || len(in.To) != 1 || in.To[0].Email != "new@example.com" || in.Subject != "You are invited to join Mail" {
		t.Errorf("message: %+v", in)
	}
	link, _ := in.Data["link"].(string)
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("token") != token || !strings.Contains(in.Text, link) {
		t.Errorf("link %q does not carry the answered token", link)
	}
	// Without a secrets key nothing is sent, and says so.
	f.svc.Cipher = nil
	if rec, out := f.doH(t, "POST", base, `{"binding": "email", "email": "other@example.com", "send": true}`, jsonHdr(f.ada)); rec.Code != 503 {
		t.Errorf("without a key: %d %v", rec.Code, out)
	}
}

// An open link never gives a capability: not even when its team gains one after the link
// was made.
func TestOpenLinkDoesNotFollowATeamIntoManaging(t *testing.T) {
	f := newRulesFixture(t)
	id := f.orgOf(t, f.ada, "Crew")
	teams := orgsPath + "/" + id + "/teams"
	rec, out := f.doH(t, "POST", teams, `{"name": "Crew", "roles": ["editor"]}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("team: %d %v", rec.Code, out)
	}
	team := out["id"].(string)
	rec, out = f.doH(t, "POST", orgsPath+"/"+id+"/invitations", `{"binding": "none", "teams": ["`+team+`"]}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("open link: %d %v", rec.Code, out)
	}
	token := out["token"].(string)
	if rec, out := f.previewInvite(t, id, token); rec.Code != 200 {
		t.Fatalf("preview: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", teams+"/"+team, `{"roles": ["owner"]}`, jsonHdr(f.ada)); rec.Code != 200 {
		t.Fatalf("team gains owner: %d %v", rec.Code, out)
	}
	if rec, out := f.previewInvite(t, id, token); rec.Code != 400 || errCode(out) != "invalid_invitation" {
		t.Errorf("preview once the team manages: %d %v", rec.Code, out)
	}
	if rec, out := f.acceptInvite(t, id, f.bob, token); rec.Code == 200 {
		t.Errorf("accept once the team manages: %d %v", rec.Code, out)
	}
}
