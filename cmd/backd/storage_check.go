package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/fernandezvara/kli"
)

type storageStep struct {
	Name   string `json:"name"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
}

type storageCheckReport struct {
	OK             bool          `json:"ok"`
	Provider       string        `json:"provider"`
	Endpoint       string        `json:"endpoint"`
	PublicEndpoint *string       `json:"public_endpoint"`
	Bucket         string        `json:"bucket"`
	Prefix         string        `json:"prefix"`
	Steps          []storageStep `json:"steps"`
	ChecksumSHA256 string        `json:"checksum_sha256"`
	Encryption     string        `json:"encryption"`
	CORS           []struct {
		Origins []string `json:"origins"`
		Methods []string `json:"methods"`
		Headers []string `json:"headers"`
	} `json:"cors"`
	LinkHost    string  `json:"link_host"`
	Storage     string  `json:"storage"`
	SentinelURL *string `json:"sentinel_url"`
}

// storageCheck handles `backd storage check`: the running backd verifies the
// realm's storage (it holds the access keys) and the report is printed. Exits 1
// when a step failed.
func storageCheck(c *kli.CommandContext) error {
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
	var reps []storageCheckReport
	var missing error
	for _, kind := range kinds {
		var rep storageCheckReport
		if err := t.adm.Storage.Check(t.ctx, url.Values{"storage": {kind}}, &rep); err != nil {
			// Checking every storage skips the one the realm doesn't have.
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
	} else {
		for i, rep := range reps {
			if i > 0 {
				fmt.Fprintln(c.Stdout())
			}
			printStorageReport(c.Stdout(), rep)
		}
	}
	for _, rep := range reps {
		if !rep.OK {
			return kli.Exit(1, fmt.Errorf("the %s storage check failed: fix the steps marked fail", rep.Storage))
		}
	}
	return nil
}

func printStorageReport(w io.Writer, rep storageCheckReport) {
	fmt.Fprintf(w, "[%s] %s  bucket %s  prefix %s  endpoint %s", rep.Storage, rep.Provider, rep.Bucket, rep.Prefix, rep.Endpoint)
	if rep.PublicEndpoint != nil {
		fmt.Fprintf(w, "  (links for %s)", *rep.PublicEndpoint)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range rep.Steps {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Level, s.Name, s.Detail)
	}
	tw.Flush()
	if rep.SentinelURL != nil {
		fmt.Fprintf(w, "default encryption: %s\nopen this address to test the public setup: %s\n", rep.Encryption, *rep.SentinelURL)
	} else {
		fmt.Fprintf(w, "signed SHA-256: %s; default encryption: %s; link host: %s\n", rep.ChecksumSHA256, rep.Encryption, rep.LinkHost)
	}
	for _, r := range rep.CORS {
		fmt.Fprintf(w, "CORS: origins %v, methods %v\n", r.Origins, r.Methods)
	}
	if rep.OK {
		fmt.Fprintln(w, "ok")
	}
}
