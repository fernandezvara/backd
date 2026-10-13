package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/fernandezvara/kli"
)

type appConfigView struct {
	Version   string         `json:"version"`
	Values    map[string]any `json:"values"`
	Overrides map[string]any `json:"overrides"`
	Invalid   []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"invalid"`
	History   []appConfigEntry `json:"history"`
	UpdatedAt string           `json:"updated_at"`
	UpdatedBy string           `json:"updated_by"`
}

type appConfigEntry struct {
	Version      string `json:"version"`
	At           string `json:"at"`
	Actor        string `json:"actor"`
	Kind         string `json:"kind"`
	RolledBackTo string `json:"rolled_back_to"`
	Changes      []struct {
		Path string `json:"path"`
		Old  any    `json:"old"`
		New  any    `json:"new"`
	} `json:"changes"`
}

type appConfigResult struct {
	Version string `json:"version"`
	Changed bool   `json:"changed"`
	Changes []struct {
		Path string `json:"path"`
		Old  any    `json:"old"`
		New  any    `json:"new"`
	} `json:"changes"`
}

// flatConfig lists the leaves of a nested document by dot path.
func flatConfig(m map[string]any, prefix string, out map[string]any) {
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flatConfig(sub, p, out)
			continue
		}
		out[p] = v
	}
}

func showValue(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// appConfigGet handles `backd app-config get`: every key with its value, or one key.
func appConfigGet(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var v appConfigView
	if err := t.call("GET", "_admin/app-config", nil, nil, &v); err != nil {
		return err
	}
	flat := map[string]any{}
	flatConfig(v.Values, "", flat)
	if key := str(c, "key"); key != "" {
		var out map[string]any
		for p, val := range flat {
			if p == key || strings.HasPrefix(p, key+".") {
				if out == nil {
					out = map[string]any{}
				}
				out[p] = val
			}
		}
		if out == nil {
			return fmt.Errorf("%q is not a configuration key", key)
		}
		flat = out
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []any{map[string]any{"version": v.Version, "values": flat, "invalid": v.Invalid}})
	}
	w := c.Stdout()
	fmt.Fprintf(w, "version %s\n", v.Version)
	paths := make([]string, 0, len(flat))
	for p := range flat {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	overrides := map[string]any{}
	flatConfig(v.Overrides, "", overrides)
	for _, p := range paths {
		mark := " "
		if _, ok := overrides[p]; ok {
			mark = "*"
		}
		fmt.Fprintf(w, "%s %-40s %s\n", mark, p, showValue(flat[p]))
	}
	fmt.Fprintln(w, "\n* differs from the default")
	for _, bad := range v.Invalid {
		fmt.Fprintf(w, "ignored: %s %s (reads as its default until fixed or reset)\n", bad.Path, bad.Reason)
	}
	return nil
}

// currentConfigVersion reads the version a change is based on: --if-match, or the live one.
func currentConfigVersion(c *kli.CommandContext, t *target) (string, error) {
	if v := str(c, "if-match"); v != "" {
		return strings.Trim(v, `"`), nil
	}
	hdr, err := t.callHeaders("GET", "_admin/app-config", nil, nil, nil, nil)
	if err != nil {
		return "", err
	}
	return strings.Trim(hdr.Get("ETag"), `"`), nil
}

func printConfigResult(c *kli.CommandContext, r appConfigResult) error {
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []appConfigResult{r})
	}
	w := c.Stdout()
	if !r.Changed {
		fmt.Fprintf(w, "nothing changed (version %s)\n", r.Version)
		return nil
	}
	for _, ch := range r.Changes {
		fmt.Fprintf(w, "%s: %s -> %s\n", ch.Path, showValue(ch.Old), showValue(ch.New))
	}
	fmt.Fprintf(w, "version %s\n", r.Version)
	return nil
}

// parseConfigValue reads a command-line value as JSON when it is, as text otherwise,
// so `true`, `10` and `["a"]` keep their types and `Back at 10:00` is a string.
func parseConfigValue(s string) any {
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err == nil && !dec.More() {
		return v
	}
	return s
}

// appConfigSet handles `backd app-config set`: one key with --key and --value, or several with --values.
func appConfigSet(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	set := map[string]any{}
	switch values, key := str(c, "values"), str(c, "key"); {
	case values != "" && key != "":
		return fmt.Errorf("use --key with --value, or --values, not both")
	case values != "":
		dec := json.NewDecoder(strings.NewReader(values))
		dec.UseNumber()
		if err := dec.Decode(&set); err != nil {
			return fmt.Errorf("--values must be a JSON object of key path to value: %w", err)
		}
	case key != "":
		set[key] = parseConfigValue(str(c, "value"))
	default:
		return fmt.Errorf("give --key and --value, or --values with a JSON object")
	}
	return patchAppConfig(c, t, map[string]any{"set": set})
}

// appConfigReset handles `backd app-config reset --keys a,b`.
func appConfigReset(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var keys []string
	for _, k := range strings.Split(str(c, "keys"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("--keys needs at least one key or group, comma separated")
	}
	return patchAppConfig(c, t, map[string]any{"reset": keys})
}

func patchAppConfig(c *kli.CommandContext, t *target, body map[string]any) error {
	version, err := currentConfigVersion(c, t)
	if err != nil {
		return err
	}
	var res appConfigResult
	if _, err := t.callHeaders("PATCH", "_admin/app-config", nil, body, &res, map[string]string{"If-Match": `"` + version + `"`}); err != nil {
		return err
	}
	return printConfigResult(c, res)
}

// appConfigHistory handles `backd app-config history`.
func appConfigHistory(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	q := url.Values{}
	if l := str(c, "limit"); l != "" {
		q.Set("limit", l)
	}
	var out struct {
		History []appConfigEntry `json:"history"`
	}
	if err := t.call("GET", "_admin/app-config/history", q, nil, &out); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), out.History)
	}
	w := c.Stdout()
	for _, e := range out.History {
		head := fmt.Sprintf("%s  %s  %s", e.Version, e.At, e.Actor)
		if e.Kind == "rollback" {
			head += "  (rollback to " + e.RolledBackTo + ")"
		}
		fmt.Fprintln(w, head)
		for _, ch := range e.Changes {
			fmt.Fprintf(w, "    %s: %s -> %s\n", ch.Path, showValue(ch.Old), showValue(ch.New))
		}
	}
	return nil
}

// appConfigRollback handles `backd app-config rollback --version c2`.
func appConfigRollback(c *kli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	version, err := currentConfigVersion(c, t)
	if err != nil {
		return err
	}
	var res appConfigResult
	body := map[string]any{"version": str(c, "version")}
	if _, err := t.callHeaders("POST", "_admin/app-config/rollback", nil, body, &res, map[string]string{"If-Match": `"` + version + `"`}); err != nil {
		return err
	}
	return printConfigResult(c, res)
}
