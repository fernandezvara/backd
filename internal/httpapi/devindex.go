package httpapi

import (
	_ "embed"
	htmltemplate "html/template"
	"net/http"
	"sort"
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
	ReloadErrs  []string
	Realms      []devindexRealm
	Links       []DevLink
	DocsURL     string
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
		if cfg.ReloadErrors != nil {
			data.ReloadErrs = cfg.ReloadErrors()
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", pageCSP)
		h.Set("X-Frame-Options", "DENY")
		if err := devindexTpl.Execute(w, data); err != nil {
			cfg.Log.Error("dev index render failed", "error", err)
		}
	}
}
