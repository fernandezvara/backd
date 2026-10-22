package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/fernandezvara/kli"
)

// secretCreate creates a field key: generated here by the server and printed once, or imported
// from --value. The key goes to standard output alone, so it can be redirected to a file.
func secretCreate(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	if typ := str(c, "type"); typ != "key" {
		return usageErr(fmt.Errorf("--type must be key (plain secrets are set with `backd secret set`), got %q", typ))
	}
	generate, src := flag(c, "generate"), str(c, "value")
	if generate == (src != "") {
		return usageErr(errors.New("give exactly one of --generate and --value"))
	}
	name := str(c, "name")
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
	uio := ioOf(c)
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	var out struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		Key         string `json:"key"`
	}
	if _, err := t.callHeaders("POST", "_admin/secrets", nil, body, &out, proof); err != nil {
		return err
	}
	if flag(c, "json") {
		enc := json.NewEncoder(uio.stdout)
		return enc.Encode(out)
	}
	if out.Key != "" {
		fmt.Fprintln(uio.stdout, out.Key)
		fmt.Fprintf(uio.stderr, "Key %s created, fingerprint %s:%s.\nThis is the only time the key is shown. Store it somewhere safe outside backd: if it is lost, every value encrypted with it is lost.\n", out.Name, out.Name, out.Fingerprint)
		return nil
	}
	fmt.Fprintf(uio.stderr, "Key %s imported, fingerprint %s:%s. Check it matches your copy.\n", out.Name, out.Name, out.Fingerprint)
	return nil
}

// keyIsHeld tells whether the realm has a key with that name, for a delete that needs a proof.
func keyIsHeld(t *target, name string) (bool, error) {
	var page struct{ Items []secret }
	if err := t.call("GET", "_admin/secrets", url.Values{}, nil, &page); err != nil {
		return false, err
	}
	for _, s := range page.Items {
		if s.Database == "" && s.Name == name && s.Type == "key" {
			return true, nil
		}
	}
	return false, nil
}
