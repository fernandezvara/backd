package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
)

// Owner-editable email templates (design/team-access.md §11): /_orgs/{org}/templates.

const (
	codeTemplateInvalid         = "invalid_template"
	codeTemplateNotCustomizable = "template_not_customizable"
	codeTemplateTestLimit       = "template_test_limit"
)

func (a *authAPI) templateRoutes(r chi.Router) {
	json := requireContentType("application/json")
	r.Get("/templates", a.listOrgTemplates)
	r.Route("/templates/{kind}/{locale}", func(r chi.Router) {
		r.Get("/", a.getOrgTemplate)
		r.With(json).Put("/", a.putOrgTemplate)
		r.Delete("/", a.deleteOrgTemplate)
		r.With(json).Post("/preview", a.previewOrgTemplate)
		r.With(json).Post("/test", a.testOrgTemplate)
	})
}

// templateError maps template errors, falling back to the organization errors.
func templateError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *auth.TemplateInvalidError
	switch {
	case errors.As(err, &invalid):
		details := make([]Detail, len(invalid.Problems))
		for i, p := range invalid.Problems {
			reason := p.Message
			if p.Line > 0 {
				reason = "line " + strconv.Itoa(p.Line) + ", column " + strconv.Itoa(p.Column) + ": " + p.Message
			}
			details[i] = Detail{Path: p.Part, Reason: reason}
		}
		writeError(w, r, http.StatusBadRequest, codeTemplateInvalid, "the template can't be used", details...)
	case errors.Is(err, auth.ErrTemplateNotCustomizable):
		writeError(w, r, http.StatusBadRequest, codeTemplateNotCustomizable, err.Error(), Detail{Path: "kind", Reason: "isn't in email.customizable"})
	case errors.Is(err, auth.ErrTemplateTestLimit):
		w.Header().Set("Retry-After", "3600")
		writeError(w, r, http.StatusTooManyRequests, codeTemplateTestLimit, err.Error())
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "the organization has no template of that kind in that language")
	default:
		orgError(w, r, err)
	}
}

func templateJSON(t auth.OrgTemplate, withText bool) map[string]any {
	out := map[string]any{"kind": t.Kind, "locale": t.Locale, "version": t.Version, "updated_at": formatTime(t.UpdatedAt)}
	if t.UpdatedBy != "" {
		out["updated_by"] = t.UpdatedBy
	}
	if withText {
		out["subject"], out["text"], out["html"] = t.Subject, t.Text, t.HTML
	}
	versions := make([]map[string]any, len(t.Versions))
	for i, v := range t.Versions {
		versions[i] = map[string]any{"version": v.Version, "at": formatTime(v.At)}
		if v.By != "" {
			versions[i]["by"] = v.By
		}
		if withText {
			versions[i]["subject"], versions[i]["text"], versions[i]["html"] = v.Subject, v.Text, v.HTML
		}
	}
	out["versions"] = versions
	return out
}

// listOrgTemplates handles GET /_orgs/{org}/templates: the kinds that can be customized, with
// what each must print, and the overrides the organization has.
func (a *authAPI) listOrgTemplates(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	list, err := svc.OrgTemplates(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"))
	if err != nil {
		templateError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, t := range list {
		items[i] = templateJSON(t, false)
	}
	kinds := map[string]any{}
	for kind, required := range svc.CustomizableKinds() {
		kinds[kind] = map[string]any{"required": nonNil(required)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"customizable": kinds, "items": items})
}

// getOrgTemplate handles GET /_orgs/{org}/templates/{kind}/{locale}.
func (a *authAPI) getOrgTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := usersOf(r).OrgTemplateOf(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "kind"), chi.URLParam(r, "locale"))
	if err != nil {
		templateError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, templateJSON(t, true))
}

// templateBody reads a template: the three parts, all of them.
func templateBody(w http.ResponseWriter, r *http.Request, obj map[string]any) (email.Override, bool) {
	f, ok := fields(w, r, obj, map[string]string{"subject": "string", "text": "string", "html": "string"})
	if !ok {
		return email.Override{}, false
	}
	return email.Override{Subject: f["subject"].(string), Text: f["text"].(string), HTML: f["html"].(string)}, true
}

// putOrgTemplate handles PUT: it checks the template (syntax with line and column, what the kind
// must print), stores it as a new version and answers with what the sanitizer would drop.
func (a *authAPI) putOrgTemplate(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	o, ok := templateBody(w, r, obj)
	if !ok {
		return
	}
	t, removed, err := usersOf(r).SaveOrgTemplate(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "kind"), chi.URLParam(r, "locale"), o)
	if err != nil {
		templateError(w, r, err)
		return
	}
	out := templateJSON(t, true)
	out["removed"] = nonNil(removed)
	writeJSON(w, http.StatusOK, out)
}

// deleteOrgTemplate handles DELETE: the kind goes back to the realm's file.
func (a *authAPI) deleteOrgTemplate(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).DeleteOrgTemplate(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "kind"), chi.URLParam(r, "locale"))
	if err != nil {
		templateError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// draftOf reads the optional draft of a preview or a test: all three parts, or none.
func draftOf(w http.ResponseWriter, r *http.Request, obj map[string]any) (draft *email.Override, ok bool) {
	_, hasSubject := obj["subject"]
	_, hasText := obj["text"]
	_, hasHTML := obj["html"]
	if !hasSubject && !hasText && !hasHTML {
		return nil, true
	}
	o, ok := templateBody(w, r, map[string]any{"subject": obj["subject"], "text": obj["text"], "html": obj["html"]})
	if !ok {
		return nil, false
	}
	return &o, true
}

// previewOrgTemplate handles POST .../preview: the message a draft (or the saved version) makes,
// with sample data or with real data (what the data hook says about the organization). Nothing
// is sent.
func (a *authAPI) previewOrgTemplate(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	mode, _ := obj["data"].(string)
	delete(obj, "data")
	if mode != "" && mode != "sample" && mode != "real" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "data", Reason: "must be sample or real"})
		return
	}
	draft, ok := draftOf(w, r, obj)
	if !ok {
		return
	}
	svc, org, kind, locale := usersOf(r), chi.URLParam(r, "org"), chi.URLParam(r, "kind"), chi.URLParam(r, "locale")
	user := principalOf(r).User
	if err := svc.CanEditTemplate(r.Context(), user.ID, org, kind, locale); err != nil {
		templateError(w, r, err)
		return
	}
	var o email.Override
	if draft != nil {
		o = *draft
	} else {
		t, err := svc.OrgTemplateOf(r.Context(), user.ID, org, kind, locale)
		if err != nil {
			templateError(w, r, err)
			return
		}
		o = t.Override
	}
	c, problems := email.CompileOverride(kind, o)
	if len(problems) > 0 {
		templateError(w, r, &auth.TemplateInvalidError{Problems: problems})
		return
	}
	d := email.SampleData(kind)
	d.Realm, d.User = chi.URLParam(r, "realm"), email.User{Email: user.Email, Locale: locale}
	if mode == "real" && a.fns != nil {
		if hook := svc.Settings.Email.DataHook; hook != "" {
			d.App = a.fns.dataHook(r.Context(), d.Realm, hook, dataHookInput{Kind: kind, Locale: locale, Org: org, User: &hookUser{ID: user.ID, Email: user.Email}})
		}
	}
	msg, removed, err := c.Render(d, svc.OrgName(r.Context(), org))
	if err != nil {
		templateError(w, r, &auth.TemplateInvalidError{Problems: []email.OverrideProblem{{Part: "template", Message: err.Error()}}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subject": msg.Subject, "text": msg.Text, "html": msg.HTML, "removed": nonNil(removed)})
}

// testOrgTemplate handles POST .../test: it sends the draft (or the saved version) to the
// requesting user's own address, marked [TEST], through the realm's delivery function.
func (a *authAPI) testOrgTemplate(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	draft, ok := draftOf(w, r, obj)
	if !ok {
		return
	}
	err := usersOf(r).QueueTemplateTest(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "org"), chi.URLParam(r, "kind"), chi.URLParam(r, "locale"), draft, requestID(r.Context()))
	if err != nil {
		templateError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}
