package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fernandezvara/kli"
)

// encryptionStatus is the answer of GET /_admin/encryption.
type encryptionStatus struct {
	Collections []struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		Complete   bool   `json:"complete"`
		Fields     []struct {
			Path        string `json:"path"`
			Configured  bool   `json:"configured"`
			Key         string `json:"key"`
			Fingerprint string `json:"fingerprint"`
			Search      bool   `json:"search"`
			Plaintext   int64  `json:"plaintext"`
			Encrypted   int64  `json:"encrypted"`
			Current     int64  `json:"current"`
			Old         int64  `json:"old"`
			Indexed     int64  `json:"indexed"`
			Complete    bool   `json:"complete"`
			Generations []struct {
				Kind       string `json:"kind"`
				Generation int    `json:"generation"`
				Status     string `json:"status"`
				Key        string `json:"key"`
				Values     int64  `json:"values"`
			} `json:"generations"`
		} `json:"fields"`
		Migration *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			DryRun bool   `json:"dry_run"`
			Counts struct {
				Scanned     int64 `json:"scanned"`
				Encrypted   int64 `json:"encrypted"`
				Reencrypted int64 `json:"reencrypted"`
				Decrypted   int64 `json:"decrypted"`
				Indexed     int64 `json:"indexed"`
				Conflicts   int64 `json:"conflicts"`
			} `json:"counts"`
		} `json:"migration"`
	} `json:"collections"`
}

func encryptScope(c *kli.CommandContext) (url.Values, error) {
	database, collection := str(c, "database"), str(c, "collection")
	if collection != "" && database == "" {
		return nil, usageErr(errors.New("--collection needs --database"))
	}
	q := url.Values{}
	if database != "" {
		q.Set("database", database)
	}
	if collection != "" {
		q.Set("collection", collection)
	}
	return q, nil
}

// encryptStatus prints, per encrypted field, how its values are stored and how far the
// migration to the configuration is.
func encryptStatus(c *kli.CommandContext) error {
	q, err := encryptScope(c)
	if err != nil {
		return err
	}
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var out encryptionStatus
	if err := t.adm.Encryption.Status(t.ctx, q, &out); err != nil {
		return err
	}
	if flag(c, "json") {
		return json.NewEncoder(c.Stdout()).Encode(out)
	}
	if len(out.Collections) == 0 {
		fmt.Fprintln(c.Stdout(), "no collection of this realm encrypts fields")
		return nil
	}
	pending := false
	for _, col := range out.Collections {
		state := "complete"
		if !col.Complete {
			state, pending = "NOT migrated", true
		}
		fmt.Fprintf(c.Stdout(), "%s/%s: %s\n", col.Database, col.Collection, state)
		tw := tabwriter.NewWriter(c.Stdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  FIELD\tKEY\tPLAINTEXT\tENCRYPTED\tCURRENT\tOLD\tINDEXED\tGENERATIONS")
		for _, f := range col.Fields {
			key := f.Key
			if f.Fingerprint != "" {
				key += ":" + f.Fingerprint
			}
			if !f.Configured {
				key = "(no longer encrypted)"
			}
			idx := "-"
			if f.Search {
				idx = fmt.Sprint(f.Indexed)
			}
			var gens []string
			for _, g := range f.Generations {
				if g.Kind == "data" {
					gens = append(gens, fmt.Sprintf("%d:%s(%d)", g.Generation, g.Status, g.Values))
				}
			}
			fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n", f.Path, key, f.Plaintext, f.Encrypted, f.Current, f.Old, idx, strings.Join(gens, " "))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		if m := col.Migration; m != nil {
			fmt.Fprintf(c.Stdout(), "  last migration %s: %s%s, %d documents read\n", m.ID, m.Status, map[bool]string{true: " (dry run)", false: ""}[m.DryRun], m.Counts.Scanned)
		}
	}
	if pending {
		fmt.Fprintln(c.Stderr(), "something is left to migrate: `backd encrypt migrate --dry-run` says what, `backd encrypt migrate` does it. Until then equality searches can miss documents.")
	}
	return nil
}

// encryptRekey starts a new generation of an encrypted field's data key.
func encryptRekey(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	uio := ioOf(c)
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	body := map[string]any{"database": str(c, "database"), "collection": str(c, "collection"), "field": str(c, "field")}
	var out struct {
		Generation int `json:"generation"`
	}
	if err := t.adm.Encryption.Rekey(t.ctx, body, &out, withProof(proof)...); err != nil {
		return err
	}
	fmt.Fprintf(uio.stdout, "%s/%s.%s: new data key, generation %d. Values written from now on use it; run `backd encrypt migrate` to seal the existing ones with it.\n",
		str(c, "database"), str(c, "collection"), str(c, "field"), out.Generation)
	return nil
}

// encryptMigrate queues the migration of a collection and follows it.
func encryptMigrate(c *kli.CommandContext) error {
	interval, err := time.ParseDuration(str(c, "interval"))
	if err != nil || interval <= 0 {
		return usageErr(errors.New("--interval must be a duration such as 2s"))
	}
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	uio := ioOf(c)
	proof, err := t.proof(uio)
	if err != nil {
		return err
	}
	body := map[string]any{"database": str(c, "database"), "collection": str(c, "collection")}
	if f := str(c, "field"); f != "" {
		body["fields"] = strings.Split(f, ",")
	}
	if flag(c, "dry-run") {
		body["dry_run"] = true
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := t.adm.Encryption.Migrate(t.ctx, body, &started, withProof(proof)...); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == 409 {
			return fmt.Errorf("%s: follow it with `backd functions jobs --realm %s`, or cancel it with `backd functions cancel`", ae.Message, t.realm)
		}
		return err
	}
	fmt.Fprintf(uio.stderr, "migration %s started\n", started.ID)
	if flag(c, "no-wait") {
		fmt.Fprintln(uio.stdout, started.ID)
		return nil
	}
	last := ""
	for {
		var job jobRecord
		if err := t.adm.Jobs.Get(t.ctx, started.ID, &job); err != nil {
			return err
		}
		if p := job.Progress; p != nil && p.Message != nil && *p.Message != last {
			fmt.Fprintln(uio.stderr, *p.Message)
			last = *p.Message
		}
		if job.Status == "done" {
			if job.Result == nil || job.Result.Status != "ok" {
				return errors.New("the migration did not finish: see `backd functions jobs --job " + started.ID + "`")
			}
			break
		}
		time.Sleep(interval)
	}
	fmt.Fprintln(uio.stdout, "done"+map[bool]string{true: " (dry run: nothing was changed)", false: ""}[flag(c, "dry-run")])
	return nil
}
