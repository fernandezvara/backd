package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/fernandezvara/cli"
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
	StaleUploads int `json:"stale_uploads"`
}

// storageUsage handles `backd storage usage`: what the realm's storage is, whether it
// works, what the documents hold in it and what waits to be cleaned up.
func storageUsage(c *cli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var st storageStatus
	if err := t.call("GET", "_admin/storage", nil, nil, &st); err != nil {
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
	fmt.Fprintf(w, "\nin use: %s in %d files\n", humanBytes(st.Usage.Bytes), st.Usage.Files)
	if len(st.Usage.Users) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "USER\tFILES\tSIZE")
		for _, u := range st.Usage.Users {
			fmt.Fprintf(tw, "%s\t%d\t%s\n", u.UserID, u.Files, humanBytes(u.Bytes))
		}
		tw.Flush()
	}
	fmt.Fprintf(w, "\nwaiting to be deleted: %d objects (%d retrying)", st.Deletions.Queued, st.Deletions.Retrying)
	if st.Deletions.Oldest != nil {
		fmt.Fprintf(w, ", the oldest since %s", *st.Deletions.Oldest)
	}
	fmt.Fprintf(w, "\nuploads left unfinished past their time: %d\n", st.StaleUploads)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

type reconcileReport struct {
	Delete           bool `json:"delete"`
	Scanned          int  `json:"scanned"`
	Referenced       int  `json:"referenced"`
	SkippedRecent    int  `json:"skipped_recent"`
	SkippedInJournal int  `json:"skipped_in_journal"`
	Ignored          int  `json:"ignored"`
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
// objects and reports (or, with --delete, removes) the ones no document references.
func storageReconcile(c *cli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var rep reconcileReport
	if err := t.call("POST", "_admin/storage/reconcile", nil, map[string]any{"delete": flag(c, "delete")}, &rep); err != nil {
		return err
	}
	if flag(c, "json") {
		if err := encodeJSONLines(c.Stdout(), []reconcileReport{rep}); err != nil {
			return err
		}
	} else {
		w := c.Stdout()
		fmt.Fprintf(w, "looked at %d objects: %d referenced by a document, %d younger than 24 hours, %d with an upload record still open, %d not shaped like files\n",
			rep.Scanned, rep.Referenced, rep.SkippedRecent, rep.SkippedInJournal, rep.Ignored)
		for _, o := range rep.Orphans {
			fmt.Fprintf(w, "orphan  %s  %s  %s\n", o.Key, humanBytes(o.Size), o.LastModified)
		}
		switch {
		case len(rep.Orphans) == 0:
			fmt.Fprintln(w, "no object is unreferenced")
		case rep.Delete:
			fmt.Fprintf(w, "deleted %d of %d unreferenced objects (%s)\n", rep.Deleted, len(rep.Orphans), humanBytes(rep.OrphanBytes))
		default:
			fmt.Fprintf(w, "%d unreferenced objects (%s): run again with --delete to remove exactly these\n", len(rep.Orphans), humanBytes(rep.OrphanBytes))
		}
	}
	if len(rep.Failed) > 0 {
		return cli.Exit(1, fmt.Errorf("%d objects could not be deleted: run it again", len(rep.Failed)))
	}
	return nil
}
