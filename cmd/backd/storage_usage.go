package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/strutil"
)

type storageStatus struct {
	Configured     bool    `json:"configured"`
	Provider       string  `json:"provider"`
	Endpoint       string  `json:"endpoint"`
	PublicEndpoint *string `json:"public_endpoint"`
	Region         string  `json:"region"`
	Bucket         string  `json:"bucket"`
	Prefix         string  `json:"prefix"`
	AccessKey      string  `json:"access_key"`
	SecretKey      string  `json:"secret_key"`
	Download       string  `json:"download"`
	PresignedTTL   string  `json:"presigned_ttl"`
	PendingTTL     string  `json:"pending_ttl"`
	Keys           struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	} `json:"keys"`
	Reachable struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	} `json:"reachable"`
	Usage struct {
		Bytes int64 `json:"bytes"`
		Files int64 `json:"files"`
		Users []struct {
			UserID string `json:"user_id"`
			Bytes  int64  `json:"bytes"`
			Files  int64  `json:"files"`
		} `json:"users"`
	} `json:"usage"`
	Deletions struct {
		Queued   int     `json:"queued"`
		Retrying int     `json:"retrying"`
		Oldest   *string `json:"oldest"`
	} `json:"deletions"`
	Quota struct {
		Realm *int64 `json:"realm"`
		User  *int64 `json:"user"`
	} `json:"quota"`
	StaleUploads int `json:"stale_uploads"`
}

// storageUsage handles `backd storage usage`: what the realm's storage is, whether it
// works, what the documents hold in it and what waits to be cleaned up.
func storageUsage(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	var st storageStatus
	if err := t.adm.Storage.Usage(t.ctx, &st); err != nil {
		return err
	}
	if flag(c, "json") {
		return encodeJSONLines(c.Stdout(), []storageStatus{st})
	}
	printStorageStatus(c.Stdout(), st)
	return nil
}

func printStorageStatus(w io.Writer, st storageStatus) {
	if !st.Configured {
		fmt.Fprintln(w, "this realm has no storage configured (storage: in realm.yaml)")
		return
	}
	fmt.Fprintf(w, "%s  bucket %s  prefix %s  endpoint %s", st.Provider, st.Bucket, st.Prefix, st.Endpoint)
	if st.PublicEndpoint != nil {
		fmt.Fprintf(w, "  (links for %s)", *st.PublicEndpoint)
	}
	fmt.Fprintf(w, "\ndownloads %s (links %s), pending uploads kept %s\n", st.Download, st.PresignedTTL, st.PendingTTL)
	keys := "ok"
	if !st.Keys.OK {
		keys = "NOT USABLE: " + st.Keys.Error
	}
	reach := "ok"
	if !st.Reachable.OK {
		reach = "NO: " + st.Reachable.Error
	}
	fmt.Fprintf(w, "access keys (%s, %s): %s\nbucket reachable: %s\n", st.AccessKey, st.SecretKey, keys, reach)
	fmt.Fprintf(w, "\nin use: %s in %d files", strutil.FormatSize(st.Usage.Bytes, strutil.SizeBinary), st.Usage.Files)
	if st.Quota.Realm != nil {
		fmt.Fprintf(w, " (%s of the realm's quota of %s)", percent(st.Usage.Bytes, *st.Quota.Realm), strutil.FormatSize(*st.Quota.Realm, strutil.SizeBinary))
	}
	fmt.Fprintln(w)
	if st.Quota.User != nil {
		fmt.Fprintf(w, "quota per user: %s\n", strutil.FormatSize(*st.Quota.User, strutil.SizeBinary))
	}
	if len(st.Usage.Users) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "USER\tFILES\tSIZE\tOF QUOTA")
		for _, u := range st.Usage.Users {
			of := ""
			if st.Quota.User != nil {
				of = percent(u.Bytes, *st.Quota.User)
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", u.UserID, u.Files, strutil.FormatSize(u.Bytes, strutil.SizeBinary), of)
		}
		tw.Flush()
	}
	fmt.Fprintf(w, "\nwaiting to be deleted: %d objects (%d retrying)", st.Deletions.Queued, st.Deletions.Retrying)
	if st.Deletions.Oldest != nil {
		fmt.Fprintf(w, ", the oldest since %s", *st.Deletions.Oldest)
	}
	fmt.Fprintf(w, "\nuploads left unfinished past their time: %d\n", st.StaleUploads)
}

type reconcileReport struct {
	Storage          string `json:"storage"`
	Delete           bool   `json:"delete"`
	Scanned          int    `json:"scanned"`
	Referenced       int    `json:"referenced"`
	SkippedRecent    int    `json:"skipped_recent"`
	SkippedInJournal int    `json:"skipped_in_journal"`
	Ignored          int    `json:"ignored"`
	Orphans          []struct {
		Key          string `json:"key"`
		Size         int64  `json:"size"`
		LastModified string `json:"last_modified"`
	} `json:"orphans"`
	OrphanBytes int64    `json:"orphan_bytes"`
	Deleted     int      `json:"deleted"`
	Failed      []string `json:"failed"`
}

// storageReconcile handles `backd storage reconcile`: the running backd lists the realm's
// objects and reports (or, with --delete, removes) the ones no document references. It does
// each storage the realm has (--storage private|public picks one).
func storageReconcile(c *kli.CommandContext) error {
	t, err := targetOf(c, false)
	if err != nil {
		return err
	}
	kinds := []string{"private", "public"}
	which := str(c, "storage")
	switch which {
	case "":
	case "private", "public":
		kinds = []string{which}
	default:
		return usageErr(errors.New("--storage must be private or public"))
	}
	var reps []reconcileReport
	var missing error
	for _, kind := range kinds {
		var rep reconcileReport
		if err := t.adm.Storage.Reconcile(t.ctx, map[string]any{"delete": flag(c, "delete"), "storage": kind}, &rep); err != nil {
			// Doing every storage skips the one the realm doesn't have.
			if ae, ok := errors.AsType[*apiError](err); ok && ae.Status == http.StatusNotFound && which == "" {
				if missing == nil {
					missing = err
				}
				continue
			}
			return err
		}
		reps = append(reps, rep)
	}
	if len(reps) == 0 {
		return missing
	}
	if flag(c, "json") {
		if err := encodeJSONLines(c.Stdout(), reps); err != nil {
			return err
		}
	}
	failed := 0
	for _, rep := range reps {
		failed += len(rep.Failed)
		if !flag(c, "json") {
			printReconcile(c.Stdout(), rep, len(reps) > 1)
		}
	}
	if failed > 0 {
		return kli.Exit(1, fmt.Errorf("%d objects could not be deleted: run it again", failed))
	}
	return nil
}

func printReconcile(w io.Writer, rep reconcileReport, labelled bool) {
	if labelled {
		fmt.Fprintf(w, "[%s]\n", rep.Storage)
	}
	fmt.Fprintf(w, "looked at %d objects: %d referenced by a document, %d younger than 24 hours, %d with an upload record still open, %d not shaped like files\n",
		rep.Scanned, rep.Referenced, rep.SkippedRecent, rep.SkippedInJournal, rep.Ignored)
	for _, o := range rep.Orphans {
		fmt.Fprintf(w, "orphan  %s  %s  %s\n", o.Key, strutil.FormatSize(o.Size, strutil.SizeBinary), o.LastModified)
	}
	switch {
	case len(rep.Orphans) == 0:
		fmt.Fprintln(w, "no object is unreferenced")
	case rep.Delete:
		fmt.Fprintf(w, "deleted %d of %d unreferenced objects (%s)\n", rep.Deleted, len(rep.Orphans), strutil.FormatSize(rep.OrphanBytes, strutil.SizeBinary))
	default:
		fmt.Fprintf(w, "%d unreferenced objects (%s): run again with --delete to remove exactly these\n", len(rep.Orphans), strutil.FormatSize(rep.OrphanBytes, strutil.SizeBinary))
	}
}

func percent(used, limit int64) string {
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", 100*float64(used)/float64(limit))
}
