package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// recorder keeps every request as "METHOD path?query" with its body and whether it carried a proof.
type recorder struct {
	log    []string
	bodies []string
	proofs []string
	reply  map[string]string // "METHOD /path" -> JSON (204 when absent)
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.log = append(r.log, req.Method+" "+strings.TrimPrefix(req.URL.Path, "/v1/r")+"?"+req.URL.RawQuery)
	r.bodies = append(r.bodies, string(b))
	r.proofs = append(r.proofs, req.Header.Get("X-Backd-Reauth"))
	out, ok := r.reply[req.Method+" "+strings.TrimPrefix(req.URL.Path, "/v1/r")]
	if !ok {
		w.WriteHeader(204)
		return
	}
	io.WriteString(w, out)
}

func recClient(t *testing.T, reply map[string]string) (*Client, *recorder) {
	r := &recorder{reply: reply}
	c := newTestClient(t, r.ServeHTTP, func(o *Options) { o.APIKey = "bdk_x" })
	c.proof.Store(&reauthProof{token: "proof", expires: time.Now().Add(time.Hour)})
	return c, r
}

const orgJSON = `{"id":"o1","name":"Acme","slug":"acme","status":"active","require_mfa":false,"sso_required":false,"session_max_age":null,"mfa_met":true,"claims":{},"created_at":"t","roles":["admin"],"effective_roles":[{"role":"admin","origin":["direct","Docs"]}],"teams":["t1"],"team_roles":{"t1":["editor"]}`

func TestOrgsCreateGetUpdateLeaveDelete(t *testing.T) {
	c, r := recClient(t, map[string]string{
		"POST /_orgs":     orgJSON + `,"session":{"token":"bds_org","expires_at":"x","org":"o1"}}`,
		"GET /_orgs/acme": orgJSON + `}`,
		"PATCH /_orgs/o1": orgJSON + `,"sso_required":true,"members_without_sso":3,"sessions_revoked":2}`,
	})
	org, err := c.Orgs().Create(bg, CreateOrgInput{Name: "Acme", Slug: "acme"})
	if err != nil || org.ID != "o1" || *org.Slug != "acme" || org.EffectiveRoles[0].Origin[1] != "Docs" || org.TeamRoles["t1"][0] != "editor" || org.SessionMaxAge != nil {
		t.Fatalf("%v %+v", err, org)
	}
	// an API key client has no slots storage requirement: its storage is memory, which has them
	oc, _ := c.Org("acme")
	if tok, _ := oc.store.Get(bg); tok != "bds_org" {
		t.Errorf("the organization's client holds %q", tok)
	}
	if got, err := c.Orgs().Get(bg, "acme"); err != nil || got.Name != "Acme" {
		t.Errorf("%v %+v", err, got)
	}
	yes := true
	name := "Acme Inc"
	upd, err := c.Orgs().Update(bg, "o1", OrgPatch{Name: &name, SSORequired: &yes})
	if err != nil || !upd.SSORequired || *upd.MembersWithoutSSO != 3 || *upd.SessionsRevoked != 2 {
		t.Fatalf("%v %+v", err, upd)
	}
	if r.bodies[2] != `{"name":"Acme Inc","sso_required":true}` || r.proofs[2] != "proof" {
		t.Errorf("update sent %s with proof %q", r.bodies[2], r.proofs[2])
	}
	c.Orgs().Update(bg, "o1", OrgPatch{Name: &name})
	if r.proofs[3] != "" {
		t.Error("renaming needs no proof")
	}
	c.Orgs().Leave(bg, "o1")
	c.Orgs().Delete(bg, "o1")
	if r.log[4] != "POST /_orgs/o1/leave?" || r.log[5] != "DELETE /_orgs/o1?" || r.proofs[5] != "proof" {
		t.Errorf("%v %v", r.log, r.proofs)
	}
}

func TestOrgsSignedIn(t *testing.T) {
	c, _ := recClient(t, nil)
	c.store = NewMemoryStorage()
	a, _ := c.Org("acme")
	b, _ := c.Org("other")
	a.auth.signedIn(bg, &Session{Token: "ta", Org: "o1", ExpiresAt: "2030"})
	b.auth.signedIn(bg, &Session{Token: "tb", Org: "o2"})
	a.auth.remember(bg, map[string]any{"name": "Acme"})
	list, err := c.Orgs().SignedIn(bg)
	if err != nil || len(list) != 2 || list[0] != (SignedInOrg{Ref: "acme", ID: "o1", Name: "Acme", ExpiresAt: "2030"}) || list[1].Ref != "other" || list[1].ID != "o2" {
		t.Errorf("%v %+v", err, list)
	}
	c2, _ := New(Options{URL: "http://x", Realm: "r", Storage: tokenOnly{}})
	if l, err := c2.Orgs().SignedIn(bg); err != nil || l != nil {
		t.Errorf("without slots: %v %v", err, l)
	}
}

func TestMembersAndTeams(t *testing.T) {
	member := `{"user":"u1","roles":["editor"],"teams":["t1"],"team_roles":[{"team":"t1","role":"lead","granted_at":"x","active":true}],"effective_roles":[],"overlap":[],"joined_at":"x"}`
	c, r := recClient(t, map[string]string{
		"GET /_orgs/acme/members":             `{"items":[` + member + `],"has_more":true,"next":"cur"}`,
		"PATCH /_orgs/acme/members/u1":        member,
		"GET /_orgs/acme/teams":               `{"items":[{"id":"t1","name":"Docs","roles":["editor"],"created_at":"x"}]}`,
		"POST /_orgs/acme/teams":              `{"id":"t2","name":"Ops","roles":[],"created_at":"x"}`,
		"PATCH /_orgs/acme/teams/t2":          `{"id":"t2","name":"Ops 2","roles":["viewer"],"created_at":"x"}`,
		"GET /_orgs/acme/teams/t1/members":    `{"items":[` + member[:len(member)-1] + `,"in_team":true,"roles_in_team":["lead"]}],"has_more":false}`,
		"POST /_orgs/acme/teams/t1/roles":     member[:len(member)-1] + `,"in_team":false,"roles_in_team":["lead"]}`,
		"PUT /_orgs/acme/teams/t1/members/u1": member,
	})
	m := c.Orgs().Members("acme")
	page, err := m.List(bg, MemberListParams{PageParams: PageParams{Limit: 5, After: "c0"}, WithoutSSO: true})
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.Next != "cur" || page.Items[0].TeamRoles[0].Role != "lead" {
		t.Fatalf("%v %+v", err, page)
	}
	if r.log[0] != "GET /_orgs/acme/members?after=c0&limit=5&sso=false" {
		t.Errorf("query = %s", r.log[0])
	}
	if u, err := m.Update(bg, "u1", []string{"editor"}); err != nil || u.User != "u1" || r.bodies[1] != `{"roles":["editor"]}` {
		t.Errorf("%v %+v %s", err, u, r.bodies[1])
	}
	m.Remove(bg, "u2")

	teams := c.Orgs().Teams("acme")
	if l, err := teams.List(bg); err != nil || len(l) != 1 || l[0].Name != "Docs" {
		t.Errorf("%v %+v", err, l)
	}
	if tm, err := teams.Create(bg, TeamInput{Name: "Ops"}); err != nil || tm.ID != "t2" {
		t.Errorf("%v %+v", err, tm)
	}
	n, roles := "Ops 2", []string{"viewer"}
	if tm, err := teams.Update(bg, "t2", TeamPatch{Name: &n, Roles: &roles}); err != nil || tm.Name != "Ops 2" {
		t.Errorf("%v %+v", err, tm)
	}
	empty := []string{}
	teams.Update(bg, "t2", TeamPatch{Roles: &empty})
	last := r.bodies[len(r.bodies)-1]
	if last != `{"roles":[]}` {
		t.Errorf("an empty list of roles must be sent: %s", last)
	}
	teams.Delete(bg, "t2")
	tmem, err := teams.Members(bg, "t1", TeamMembersParams{Role: "lead", PageParams: PageParams{Limit: 2}})
	if err != nil || !tmem.Items[0].InTeam || tmem.Items[0].RolesInTeam[0] != "lead" || tmem.Items[0].User != "u1" {
		t.Errorf("%v %+v", err, tmem)
	}
	g, err := teams.GrantRole(bg, "t1", TeamRoleInput{User: "u1", Role: "lead", Until: "2030-01-01T00:00:00Z"})
	if err != nil || g.InTeam {
		t.Errorf("%v %+v", err, g)
	}
	teams.SetRoleWindow(bg, "t1", TeamRoleInput{User: "u1", Role: "lead"})
	teams.RevokeRole(bg, "t1", TeamRoleInput{User: "u1", Role: "lead"})
	teams.AddMember(bg, "t1", "u1")
	teams.RemoveMember(bg, "t1", "u1")
	want := []string{"POST /_orgs/acme/teams/t1/roles?", "PATCH /_orgs/acme/teams/t1/roles?", "DELETE /_orgs/acme/teams/t1/roles?", "PUT /_orgs/acme/teams/t1/members/u1?", "DELETE /_orgs/acme/teams/t1/members/u1?"}
	got := r.log[len(r.log)-5:]
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %s; want %s", i, got[i], want[i])
		}
	}
	var in map[string]any
	json.Unmarshal([]byte(r.bodies[len(r.bodies)-5]), &in)
	if in["until"] != "2030-01-01T00:00:00Z" || in["from"] != nil {
		t.Errorf("grant sent %v", in)
	}
}

func TestTemplatesAndInvitations(t *testing.T) {
	tmpl := `{"kind":"invitation","locale":"en","version":2,"updated_at":"x","subject":"Hi","text":"t","html":"<p>h</p>","versions":[{"version":1,"at":"y","by":"u1","subject":"Old"}],"removed":["script"]}`
	inv := `{"id":"i1","binding":"email","status":"pending","email":"a@b.c","roles":["editor"],"teams":[],"max_uses":1,"uses":0,"created_at":"x","expires_at":"y","token":"tok","link":"https://app/join?t=tok"}`
	c, r := recClient(t, map[string]string{
		"GET /_orgs/acme/templates":                        `{"customizable":{"invitation":{"required":["link"]}},"items":[` + tmpl + `]}`,
		"GET /_orgs/acme/templates/invitation/en":          tmpl,
		"PUT /_orgs/acme/templates/invitation/en":          tmpl,
		"POST /_orgs/acme/templates/invitation/en/preview": `{"subject":"S","text":"T","html":"<p>H</p>","removed":[]}`,
		"POST /_orgs/acme/invitations":                     inv,
		"GET /_orgs/acme/invitations":                      `{"items":[` + inv[:strings.Index(inv, `,"token"`)] + `}]}`,
		"POST /_orgs/acme/invitations/i1/link":             inv,
	})
	tp := c.Orgs().Templates("acme")
	l, err := tp.List(bg)
	if err != nil || l.Customizable["invitation"].Required[0] != "link" || l.Items[0].Versions[0].Subject != "Old" || l.Items[0].Versions[0].By != "u1" {
		t.Fatalf("%v %+v", err, l)
	}
	if got, err := tp.Get(bg, "invitation", "en"); err != nil || got.Subject != "Hi" || got.Removed[0] != "script" {
		t.Errorf("%v %+v", err, got)
	}
	if _, err := tp.Put(bg, "invitation", "en", TemplateText{Subject: "S", Text: "T", HTML: "H"}); err != nil {
		t.Error(err)
	}
	pv, err := tp.Preview(bg, "invitation", "en", PreviewInput{Draft: &TemplateText{Subject: "S"}, Data: map[string]any{"link": "x"}})
	if err != nil || pv.Subject != "S" || pv.HTML != "<p>H</p>" {
		t.Errorf("%v %+v", err, pv)
	}
	if body := r.bodies[3]; body != `{"data":{"link":"x"},"html":"","subject":"S","text":""}` {
		t.Errorf("preview sent %s", body)
	}
	tp.Test(bg, "invitation", "en", nil)
	tp.Delete(bg, "invitation", "en")

	iv := c.Orgs().Invitations("acme")
	made, err := iv.Create(bg, InviteInput{Binding: "email", Email: "a@b.c", Roles: []string{"editor"}, Send: true, ExpiresIn: "2d"})
	if err != nil || made.Token != "tok" || *made.Link != "https://app/join?t=tok" || made.Status != "pending" {
		t.Fatalf("%v %+v", err, made)
	}
	if r.bodies[6] != `{"binding":"email","email":"a@b.c","roles":["editor"],"send":true,"expires_in":"2d"}` {
		t.Errorf("invite sent %s", r.bodies[6])
	}
	if list, err := iv.List(bg); err != nil || len(list) != 1 || list[0].ID != "i1" {
		t.Errorf("%v %+v", err, list)
	}
	if n, err := iv.NewLink(bg, "i1"); err != nil || n.Token != "tok" {
		t.Errorf("%v %+v", err, n)
	}
	iv.Send(bg, "i1")
	iv.Revoke(bg, "i1")
	if r.log[len(r.log)-2] != "POST /_orgs/acme/invitations/i1/send?" || r.log[len(r.log)-1] != "DELETE /_orgs/acme/invitations/i1?" {
		t.Errorf("%v", r.log[len(r.log)-2:])
	}
}

func TestSSOConnections(t *testing.T) {
	conn := `{"provider":"google","client_id":"cid","hosted_domain":"acme.com","email_domains":["acme.com"],"native_client_ids":[],"join_roles":["member"],"enabled":true,"client_secret_set":true,"redirect_path":"/cb","created_at":"x","updated_at":"y"}`
	c, r := recClient(t, map[string]string{
		"GET /_orgs/acme/sso":        `{"items":[` + conn + `],"providers":["google","microsoft"],"max_connections":2}`,
		"GET /_orgs/acme/sso/google": conn,
		"PUT /_orgs/acme/sso/google": conn,
	})
	sso := c.Orgs().SSO("acme")
	l, err := sso.List(bg)
	if err != nil || l.MaxConnections != 2 || l.Items[0].HostedDomain != "acme.com" || !l.Items[0].ClientSecretSet {
		t.Fatalf("%v %+v", err, l)
	}
	if got, err := sso.Get(bg, "google"); err != nil || got.JoinRoles[0] != "member" {
		t.Errorf("%v %+v", err, got)
	}
	yes := true
	if _, err := sso.Set(bg, "google", ConnectionInput{ClientID: "cid", ClientSecret: "s3cr3t", HostedDomain: "acme.com", Enabled: &yes}); err != nil {
		t.Error(err)
	}
	if r.bodies[2] != `{"client_id":"cid","client_secret":"s3cr3t","enabled":true,"hosted_domain":"acme.com"}` || r.proofs[2] != "proof" {
		t.Errorf("set sent %s with proof %q", r.bodies[2], r.proofs[2])
	}
	if err := sso.Delete(bg, "google"); err != nil || r.proofs[3] != "proof" {
		t.Errorf("%v %q", err, r.proofs[3])
	}
}
