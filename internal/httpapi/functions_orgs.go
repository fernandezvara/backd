package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Organizations for functions with admin access (design/organizations.md §14): ctx.admin.orgs.
// The routes answer on the internal listener, to the callback token of a function that has
// admin access, and share their handlers with the admin API: what the function does is
// recorded in the audit trail with the function as the actor.

// orgRoutes mounts /v1/{realm}/_fn/orgs on the internal listener.
func (f *functions) orgRoutes(r chi.Router) {
	adm := &adminAPI{users: f.docs.users, reg: f.docs.reg, fns: f}
	json := requireContentType("application/json")
	r.Route("/v1/{realm}/_fn/orgs", func(r chi.Router) {
		r.Use(noStore, f.adminFunction)
		r.Get("/", adm.listOrgs)
		r.With(json).Post("/", adm.createOrg)
		r.Route("/{org}", func(r chi.Router) {
			r.Get("/", adm.getOrg)
			r.Get("/members", adm.orgMembers)
			r.With(json).Post("/members", adm.addOrgMember)
			r.With(json).Patch("/members/{user}", adm.setOrgMemberRoles)
			r.Delete("/members/{user}", adm.removeOrgMember)
			r.With(json).Put("/claims", adm.setOrgClaims)
			r.With(json).Post("/owner", adm.assignOrgOwner)
			r.Get("/teams", adm.listOrgTeams)
			r.With(json).Post("/teams", adm.createOrgTeam)
			r.With(json).Patch("/teams/{team}", adm.updateOrgTeam)
			r.Delete("/teams/{team}", adm.deleteOrgTeam)
			r.Get("/teams/{team}/members", adm.orgTeamMembers)
			r.Put("/teams/{team}/members/{user}", adm.setOrgTeamMember(true))
			r.Delete("/teams/{team}/members/{user}", adm.setOrgTeamMember(false))
			r.Get("/invitations", adm.listOrgInvitations)
			r.With(json).Post("/invitations", adm.createOrgInvitation)
			r.Delete("/invitations/{id}", adm.revokeOrgInvitation)
			r.Post("/invitations/{id}/link", adm.newOrgInvitationLink)
		})
	})
}

// adminFunction admits only the callback token of a function with admin access, in a realm
// with organizations, and gives the handlers the realm's users service.
func (f *functions) adminFunction(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realm := chi.URLParam(r, "realm")
		caller, _, ok := f.docs.identify(w, r, realm)
		if !ok {
			return
		}
		if caller.Func == nil || !caller.Func.Admin {
			writeError(w, r, http.StatusForbidden, codeForbidden, "this function didn't declare `admin: true` in its function.yaml")
			return
		}
		svc := f.docs.users(realm)
		if svc == nil || !svc.OrgsEnabled() {
			writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no organizations")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), usersKey{}, svc)))
	})
}
