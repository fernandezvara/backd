package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
)

// Direct mode (`backd export|import --direct`) runs the same admin API the network path
// uses, in this process, over MongoDB and CONFIG_DIR: no server is needed. The requests never
// touch a socket: they are handed to the handler, which takes each of them as a full
// administrator's working offline (and says so in the records it writes).

// directFlags are the flags the commands that can run directly share.
func directFlags(cc *kli.CommandConfig) {
	boolean(cc, "direct", "work straight on MongoDB and CONFIG_DIR, without a running backd (the realm needs auth enabled)")
	optional(cc, "mongo-uri", "with --direct: the MongoDB URI, or env:<VAR> / file:<path> to read it from (default: BACKD_MONGO_URI, then MONGO_URI)")
	optional(cc, "master-key", "with --direct: the secrets master key (env:<VAR> / file:<path>): needed for files kept with the realm's storage secrets and for authentication data sealed by the realm; default BACKD_SECRETS_KEY")
	optional(cc, "storage-key", "with --direct: the storage access key (env:<VAR> / file:<path>) instead of the realm's secrets")
	optional(cc, "storage-secret", "with --direct: the storage secret key (env:<VAR> / file:<path>) instead of the realm's secrets")
	boolean(cc, "force", "with --direct import: write even while a backd instance serves the realm")
}

// usesDirect reports whether the command runs directly, refusing the flags that only make
// sense with it otherwise.
func usesDirect(c *kli.CommandContext) (bool, error) {
	if flag(c, "direct") {
		return true, nil
	}
	for _, f := range []string{"mongo-uri", "master-key", "storage-key", "storage-secret"} {
		if str(c, f) != "" {
			return false, usageErr(fmt.Errorf("--%s needs --direct", f))
		}
	}
	if flag(c, "force") {
		return false, usageErr(errors.New("--force needs --direct"))
	}
	return false, nil
}

// direct is a realm opened for offline work.
type direct struct {
	app *app
	t   *target
}

func (d *direct) close() { d.app.close() }

// valueOrSource reads a flag that holds a value or key:/env:/file:, returning "" when unset.
func valueOrSource(c *kli.CommandContext, name string) (string, error) {
	spec := str(c, name)
	if spec == "" {
		return "", nil
	}
	v, err := readKeySource(spec, c.Getenv, c.Stderr())
	if err != nil {
		return "", usageErr(fmt.Errorf("--%s: %w", name, err))
	}
	return v, nil
}

// openDirect connects to MongoDB and loads CONFIG_DIR for the realm named by --realm.
func openDirect(c *kli.CommandContext) (*direct, error) {
	realm := str(c, "realm")
	uri := ""
	if spec := str(c, "mongo-uri"); spec != "" {
		if strings.HasPrefix(spec, "mongodb://") || strings.HasPrefix(spec, "mongodb+srv://") {
			uri = spec
		} else {
			v, err := valueOrSource(c, "mongo-uri")
			if err != nil {
				return nil, err
			}
			uri = v
		}
	}
	masterKey, err := valueOrSource(c, "master-key")
	if err != nil {
		return nil, err
	}
	storageKey, err := valueOrSource(c, "storage-key")
	if err != nil {
		return nil, err
	}
	storageSecret, err := valueOrSource(c, "storage-secret")
	if err != nil {
		return nil, err
	}
	if (storageKey == "") != (storageSecret == "") {
		return nil, usageErr(errors.New("--storage-key and --storage-secret go together"))
	}
	env := func(k string) string {
		switch k {
		case "MONGO_URI":
			if uri != "" {
				return uri
			}
			return firstSet(c.Getenv("BACKD_MONGO_URI"), c.Getenv("MONGO_URI"))
		case "BACKD_SECRETS_KEY":
			if masterKey != "" {
				return masterKey
			}
		case "LOG_LEVEL":
			return "warn"
		}
		return c.Getenv(k)
	}
	ctx := context.Background()
	cfg, err := settings.Load(env)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewJSONHandler(c.Stderr(), &slog.HandlerOptions{Level: cfg.LogLevel}))
	// The functions' bundles are not needed to move data: the config is read, not built.
	reg, err := registry.Load(cfg.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	fingerprint, err := reg.Fingerprint()
	if err != nil {
		return nil, err
	}
	rl := reg.Realms[realm]
	if rl == nil {
		return nil, fmt.Errorf("CONFIG_DIR (%s) has no realm %q", cfg.ConfigDir, realm)
	}
	if !rl.Settings.AuthEnabled {
		return nil, fmt.Errorf("realm %q has auth disabled, and the export and import tools work through the admin API of a realm with auth enabled", realm)
	}
	client, err := mongodb.Connect(ctx, cfg.MongoURI)
	if err != nil {
		return nil, err
	}
	a := &app{cfg: cfg, log: log, reg: reg, client: client, fingerprint: fingerprint}
	hc := a.handlerConfig()
	hc.Offline = true
	hc.StorageAccessKey, hc.StorageSecretKey = storageKey, storageSecret
	h := httpapi.NewHandler(hc)
	return &direct{app: a, t: &target{url: "http://backd.offline", realm: realm, fromKey: true, http: &http.Client{Transport: inProcess{h}}}}, nil
}

func firstSet(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// refuseIfServed fails while a backd instance that serves the realm is alive, unless force.
func (d *direct) refuseIfServed(ctx context.Context, force bool, stderr io.Writer) error {
	ids, err := mongodb.ActiveInstances(ctx, d.app.client, d.t.realm, time.Now())
	if err != nil {
		return fmt.Errorf("could not check whether the realm is served: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if force {
		fmt.Fprintf(stderr, "warning: %d running backd instance(s) serve realm %q; --force writes anyway\n", len(ids), d.t.realm)
		return nil
	}
	return fmt.Errorf("realm %q is served by %d running backd instance(s): stop them (or --force, if you know they are not writing) before importing offline", d.t.realm, len(ids))
}

// inProcess is an http.RoundTripper that hands requests to a handler without a network. The
// response body streams: the handler runs on its own goroutine and writes into a pipe.
type inProcess struct{ h http.Handler }

func (p inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	r := req.Clone(req.Context())
	r.RemoteAddr = "127.0.0.1:0"
	r.RequestURI = req.URL.RequestURI()
	if r.Body == nil {
		r.Body = http.NoBody
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					_ = pw.CloseWithError(errors.New("the stream was cut"))
				} else {
					_ = pw.CloseWithError(fmt.Errorf("handler panic: %v", rec))
				}
				w.WriteHeader(http.StatusInternalServerError) // only if nothing was sent yet
				return
			}
			w.WriteHeader(http.StatusOK) // a handler that wrote nothing
			_ = pw.Close()
		}()
		p.h.ServeHTTP(w, r)
	}()
	select {
	case <-w.ready:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	resp := &http.Response{
		Status: fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)), StatusCode: w.status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: w.header.Clone(), Body: pr, Request: req, ContentLength: -1,
	}
	return resp, nil
}

// pipeWriter is the ResponseWriter of an in-process request.
type pipeWriter struct {
	header http.Header
	pw     *io.PipeWriter
	status int
	once   sync.Once
	ready  chan struct{}
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.status = status
		close(w.ready)
	})
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}

func (w *pipeWriter) Flush() {}
