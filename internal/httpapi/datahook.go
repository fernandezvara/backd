package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/registry"
)

// Template data hooks (design/team-access.md §10): an internal, sync function the realm names in
// email.data_hook and pages.data_hook, called before a system email or a hosted page is rendered.
// What it returns is available to the template as {{.App}}. A hook never blocks or breaks the
// message: it is bounded in time and size, and on any failure rendering goes on without App.
const (
	dataHookTimeout = 2 * time.Second
	maxDataHook     = 16 << 10
)

// hookUser is who a data hook is called for: "" fields are left out.
type hookUser struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email,omitempty"`
}

// dataHookInput is what a data hook receives.
type dataHookInput struct {
	Kind   string    `json:"kind"`
	Locale string    `json:"locale"`
	User   *hookUser `json:"user,omitempty"`
	Org    string    `json:"org,omitempty"`
}

// dataHook calls the function ref (<database>/<function>) of a realm and returns its object
// result, or nil: when there is no hook, it can't run, it fails, it takes more than two seconds
// or answers more than 16 KiB or something that isn't an object. Failures are logged, never
// returned.
func (f *functions) dataHook(ctx context.Context, realm, ref string, in dataHookInput) map[string]any {
	if ref == "" || f.runner == nil {
		return nil
	}
	database, name, _ := strings.Cut(ref, "/")
	log := logger(ctx).With("hook", realm+"/"+ref, "kind", in.Kind)
	fn := f.lookup(realm, database, name)
	if fn == nil || fn.Mode != registry.ModeSync {
		log.Warn("template data hook is not a sync function of the realm")
		return nil
	}
	hash, built := f.resolveHash(realm + "/" + database + "/" + name)
	if !built {
		log.Warn("template data hook is not built")
		return nil
	}
	svc := f.docs.users(realm)
	var secrets map[string]string
	if len(fn.Secrets) > 0 && svc != nil {
		values, missing, err := svc.ResolveSecrets(ctx, fn.Secrets, database)
		if err != nil || len(missing) > 0 {
			log.Warn("template data hook's secrets are not available", "error", err, "missing", missing)
			return nil
		}
		secrets = values
	}
	if f.concurrency != nil {
		key := realm + "/" + database + "/" + name
		if !f.concurrency.tryAcquire(key, fn.Concurrency) {
			log.Warn("template data hook is at its concurrency limit")
			return nil
		}
		defer f.concurrency.release(key, fn.Concurrency)
	}
	input, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	invID := xid.New().String()
	deadline := f.docs.now().Add(dataHookTimeout)
	res, err := f.runner.Invoke(ctx, executor.InvokeRequest{
		Function:  realm + "/" + database + "/" + name,
		Bundle:    executor.Bundle{SHA256: hash, URL: strings.TrimRight(f.callbackURL, "/") + "/_internal/functions/" + hash},
		TimeoutMS: dataHookTimeout.Milliseconds(),
		MemoryMB:  (fn.Memory + 1<<20 - 1) >> 20,
		MaxOutput: min(fn.MaxOutput, maxDataHook),
		Network:   fn.Network,
		Envelope: executor.Envelope{
			Mode:      fn.Mode,
			Input:     input,
			Secrets:   secrets,
			Callback:  f.callback(realm, database, name, fn, auth.Caller{}, deadline.Add(callbackMargin), invID, 0, nil),
			RequestID: requestID(ctx),
		},
	})
	if err != nil {
		log.Warn("template data hook could not run", "error", err)
		return nil
	}
	f.recordInvocation(ctx, requestID(ctx), realm, database, name, fn.Mode, auth.Caller{}, res, "", invID, callMeta{Origin: "data_hook"})
	if res.Status != executor.StatusOK {
		log.Warn("template data hook failed", "status", res.Status)
		return nil
	}
	return decodeHookData(log, res.Output)
}

// pageApp fills in d.App with what the realm's pages.data_hook says about the company of a page.
// Only a page that has a company context (it comes from a token or request that carried one)
// calls the hook: pages are public, and a page that anyone can open, like the one of a wrong
// token, must not make the app's function run. A page the hook can't serve is shown without.
func (f *functions) pageApp(ctx context.Context, realm, kind string, d *email.PageData, org, address string) {
	hook := f.docs.reg.Realms[realm].Settings.PagesDataHook
	if hook == "" || org == "" {
		return
	}
	in := dataHookInput{Kind: kind, Locale: d.Locale, Org: org}
	if address != "" {
		in.User = &hookUser{Email: address}
	}
	d.App = f.dataHook(ctx, realm, hook, in)
}

// decodeHookData reads a data hook's result: an object of at most maxDataHook bytes.
func decodeHookData(log interface{ Warn(string, ...any) }, output json.RawMessage) map[string]any {
	if len(output) > maxDataHook {
		log.Warn("template data hook's answer is too large", "bytes", len(output))
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(output))
	var data map[string]any
	if err := dec.Decode(&data); err != nil || data == nil {
		log.Warn("template data hook didn't answer an object")
		return nil
	}
	return data
}
