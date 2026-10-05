// Package adminui serves the admin web interface under /_ui/. The built
// assets (ui/admin, `npm run build`) are embedded from dist/; a Go build
// without them still compiles, and Handler then returns nil so /_ui/
// answers 404 like any unknown route.
package adminui

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var assets embed.FS

// Prefix is where the interface is served.
const Prefix = "/_ui"

// csp lets the page load only its own scripts, styles and images, and talk
// only to this origin. No inline script or style, no eval.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Handler serves the embedded interface, or returns nil when this build has
// no assets. idle is the sign-out timeout handed to the page.
func Handler(idle time.Duration) http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil
	}
	return newHandler(sub, idle)
}

func newHandler(fsys fs.FS, idle time.Duration) http.Handler {
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		return nil
	}
	config, _ := json.Marshal(map[string]int{"idle_seconds": int(idle / time.Second)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, Prefix), "/")
		if rel == "config.json" {
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "no-store")
			_, _ = w.Write(config)
			return
		}
		// Hashed build output never changes under its name.
		if strings.HasPrefix(rel, "assets/") {
			if f, err := fsys.Open(rel); err == nil {
				defer f.Close()
				if st, err := f.Stat(); err == nil && !st.IsDir() {
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
					serve(w, r, rel, f, st)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		// Anything else that is a real file (favicon, ...) revalidates; every
		// other path is a client-side route: the page itself.
		name := "index.html"
		if rel != "" && !strings.HasSuffix(rel, "/") && path.Ext(rel) != "" {
			if st, err := fs.Stat(fsys, rel); err == nil && !st.IsDir() {
				name = rel
			} else {
				http.NotFound(w, r)
				return
			}
		}
		f, err := fsys.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if name == "index.html" {
			h.Set("Cache-Control", "no-store")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		serve(w, r, name, f, st)
	})
}

func serve(w http.ResponseWriter, r *http.Request, name string, f fs.File, st fs.FileInfo) {
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
}
