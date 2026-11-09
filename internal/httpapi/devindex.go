package httpapi

import (
	"cmp"
	_ "embed"
	htmltemplate "html/template"
	"log/slog"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/devreport"
)

// The dev landing page lives at GET / when BACKD_DEV=true: it lists the
// loaded realms and their databases, and links the things a developer
// opens next (the admin UI per realm, each realm's OpenAPI, the
// health/readiness probes and the docs). It only exists in dev mode and
// the whole handler is rebuilt on every live reload, so the page always
// shows the config currently serving.

//go:embed devindex.html
var devindexTemplate string

// DevLink is one extra link the page shows, from BACKD_DEV_LINKS
// (Label=URL;Label=URL, parsed in cmd/backd): the local tools next to the
// API, like a MinIO console, that backd knows nothing about otherwise.
type DevLink struct {
	Label string
	URL   string
}

type devindexData struct {
	UI          bool
	Fingerprint string
	// Problems are why the config files don't load: the last reload's, or, while Waiting,
	// what keeps the first config from loading.
	Problems []devreport.Problem
	Waiting  bool
	Realms   []devindexRealm
	Links    []DevLink
	DocsURL  string
}

type devindexRealm struct {
	Name      string
	Databases []devindexDatabase
}

type devindexDatabase struct {
	Name        string
	Collections int
	Functions   int
}

var devindexTpl = htmltemplate.Must(htmltemplate.New("devindex").Parse(devindexTemplate))

// devIndex serves the dev landing page.
func devIndex(cfg Config) http.HandlerFunc {
	data := devindexData{UI: cfg.UI != nil, DocsURL: "https://fernandezvara.github.io/backd/docs/", Fingerprint: cfg.ConfigFingerprint}
	data.Links = cfg.DevLinks
	for name, rl := range cfg.Registry.Realms {
		realm := devindexRealm{Name: name}
		for dbName, db := range rl.Databases {
			fns := 0
			if db.Functions != nil {
				fns = len(db.Functions.Functions)
			}
			realm.Databases = append(realm.Databases, devindexDatabase{Name: dbName, Collections: len(db.Collections), Functions: fns})
		}
		sort.Slice(realm.Databases, func(i, j int) bool { return realm.Databases[i].Name < realm.Databases[j].Name })
		data.Realms = append(data.Realms, realm)
	}
	sort.Slice(data.Realms, func(i, j int) bool { return data.Realms[i].Name < data.Realms[j].Name })
	return func(w http.ResponseWriter, r *http.Request) {
		page := data
		if cfg.DevProblems != nil {
			page.Problems = cfg.DevProblems()
		}
		renderDevIndex(w, http.StatusOK, page, cfg.Log)
	}
}

func renderDevIndex(w http.ResponseWriter, status int, data devindexData, log *slog.Logger) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	if err := devindexTpl.Execute(w, data); err != nil && log != nil {
		log.Error("dev index render failed", "error", err)
	}
}

// WaitingConfig is what a dev-mode server answers with before any config has loaded.
type WaitingConfig struct {
	// Problems are why the config doesn't load yet; called on every request.
	Problems func() []devreport.Problem
	UI       http.Handler // the admin UI, served so it can say why nothing works; nil without
	Links    []DevLink
	Log      *slog.Logger
}

// NewWaitingHandler serves a dev-mode instance whose config hasn't loaded yet (BACKD_DEV): GET /
// is the landing page with the report only, /healthz is up (the process is fine; the
// container must not be restarted), /readyz is not ready, the admin UI is served, and
// everything else, every /v1/... route included, answers 503 config_not_loaded with the
// report in the error's details.
func NewWaitingHandler(cfg WaitingConfig) http.Handler {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	r := chi.NewRouter()
	r.Use(withRequestID(cfg.Log), securityHeaders)
	problems := func() []devreport.Problem {
		if cfg.Problems == nil {
			return nil
		}
		return cfg.Problems()
	}
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		renderDevIndex(w, http.StatusServiceUnavailable, devindexData{
			UI: cfg.UI != nil, Waiting: true, Problems: problems(), Links: cfg.Links,
			DocsURL: "https://fernandezvara.github.io/backd/docs/",
		}, cfg.Log)
	})
	if cfg.UI != nil {
		r.Handle("/_ui", cfg.UI)
		r.Handle("/_ui/*", cfg.UI)
	}
	notLoaded := func(w http.ResponseWriter, r *http.Request) {
		ps := problems()
		details := make([]Detail, 0, len(ps))
		for _, p := range ps {
			reason := p.Message
			if p.Output != "" {
				reason += "\n" + p.Output
			}
			reason += "\n" + p.Hint
			details = append(details, Detail{Path: cmp.Or(p.Location(), "CONFIG_DIR"), Reason: reason, Code: p.Kind})
		}
		msg := "the config has not loaded yet (dev mode): fix what the report says and save; backd starts on the first change that loads. The report is on this server's root page."
		w.Header().Set("Retry-After", "2")
		writeError(w, r, http.StatusServiceUnavailable, codeConfigNotLoaded, msg, details...)
	}
	r.NotFound(notLoaded)
	r.MethodNotAllowed(notLoaded)
	return r
}
