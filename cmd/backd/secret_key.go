package main

import (
	"errors"
	"fmt"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// secretCreate creates a field key: generated here by the server and printed once, or imported
// from --value. The key goes to standard output alone, so it can be redirected to a file.
func secretCreate(c *kli.CommandContext) error {
	t, closeT, _, err := openTarget(c)
	if err != nil {
		return err
	}
	defer closeT()
	if typ := cmdutil.Str(c, "type"); typ != "key" {
		return cmdutil.UsageErr(fmt.Errorf("--type must be key (plain secrets are set with `backd secret set`), got %q", typ))
	}
	generate, src := cmdutil.Flag(c, "generate"), cmdutil.Str(c, "value")
	if generate == (src != "") {
		return cmdutil.UsageErr(errors.New("give exactly one of --generate and --value"))
	}
	name := cmdutil.Str(c, "name")
	body := map[string]any{"name": name, "type": "key"}
	if generate {
		body["generate"] = true
	} else {
		v, err := readKeySource(src, c.Getenv, c.Stderr())
		if err != nil {
			return err
		}
		body["value"] = v
	}
	uio := cmdutil.IOOf(c)
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	var out struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		Key         string `json:"key"`
	}
	if err := t.adm.Secrets.Create(t.ctx, body, &out, withProof(proof)...); err != nil {
		var ae *apiError
		if cmdutil.Flag(c, "skip-existing") && errors.As(err, &ae) && ae.Status == 409 {
			fmt.Fprintf(uio.Stderr, "Key %s exists already: left as it is.\n", name)
			return nil
		}
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSON(uio.Stdout, out)
	}
	if out.Key != "" {
		fmt.Fprintln(uio.Stdout, out.Key)
		fmt.Fprintf(uio.Stderr, "Key %s created, fingerprint %s:%s.\nThis is the only time the key is shown. Store it somewhere safe outside backd: if it is lost, every value encrypted with it is lost.\n", out.Name, out.Name, out.Fingerprint)
		return nil
	}
	fmt.Fprintf(uio.Stderr, "Key %s imported, fingerprint %s:%s. Check it matches your copy.\n", out.Name, out.Name, out.Fingerprint)
	return nil
}

// keyIsHeld tells whether the realm has a key with that name, for a delete that needs a proof.
func keyIsHeld(t *target, name string) (bool, error) {
	var page struct{ Items []secret }
	if err := t.adm.Secrets.List(t.ctx, &page); err != nil {
		return false, err
	}
	for _, s := range page.Items {
		if s.Database == "" && s.Org == "" && s.Name == name && s.Type == "key" {
			return true, nil
		}
	}
	return false, nil
}

// secretRotate gives a key a new value and re-wraps what it protects. A generated key goes to
// standard output once, like a created one.
func secretRotate(c *kli.CommandContext) error {
	t, closeT, _, err := openTarget(c)
	if err != nil {
		return err
	}
	defer closeT()
	generate, resume, src := cmdutil.Flag(c, "generate"), cmdutil.Flag(c, "resume"), cmdutil.Str(c, "value")
	n := 0
	for _, set := range []bool{generate, resume, src != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return cmdutil.UsageErr(errors.New("give exactly one of --generate, --value and --resume"))
	}
	body := map[string]any{}
	switch {
	case generate:
		body["generate"] = true
	case resume:
		body["resume"] = true
	default:
		v, err := readKeySource(src, c.Getenv, c.Stderr())
		if err != nil {
			return err
		}
		body["value"] = v
	}
	uio := cmdutil.IOOf(c)
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	name := cmdutil.Str(c, "name")
	var out struct {
		Fingerprint string `json:"fingerprint"`
		Previous    string `json:"previous_fingerprint"`
		Rewrapped   int    `json:"rewrapped"`
		Key         string `json:"key"`
	}
	if err := t.adm.Secrets.Rotate(t.ctx, name, body, &out, withProof(proof)...); err != nil {
		return err
	}
	if cmdutil.Flag(c, "json") {
		return cmdutil.PrintJSON(uio.Stdout, out)
	}
	if out.Key != "" {
		fmt.Fprintln(uio.Stdout, out.Key)
		fmt.Fprintf(uio.Stderr, "Key %s rotated: %s:%s replaces %s:%s, %d data key(s) wrapped again, no document rewritten.\nThis is the only time the new key is shown. Store it somewhere safe outside backd, and keep the old one for backups made before today.\n", name, name, out.Fingerprint, name, out.Previous, out.Rewrapped)
		return nil
	}
	fmt.Fprintf(uio.Stderr, "Key %s rotated: %s:%s replaces %s:%s, %d data key(s) wrapped again, no document rewritten.\n", name, name, out.Fingerprint, name, out.Previous, out.Rewrapped)
	return nil
}
