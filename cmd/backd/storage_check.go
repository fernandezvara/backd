package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/fernandezvara/cli"
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
	LinkHost string `json:"link_host"`
}

// storageCheck handles `backd storage check`: the running backd verifies the
// realm's storage (it holds the access keys) and the report is printed. Exits 1
// when a step failed.
func storageCheck(c *cli.CommandContext) error {
	t, err := newTarget(str(c, "url"), str(c, "realm"), c.Getenv, false)
	if err != nil {
		return err
	}
	var rep storageCheckReport
	if err := t.call("POST", "_admin/storage/check", nil, nil, &rep); err != nil {
		return err
	}
	if flag(c, "json") {
		if err := encodeJSONLines(c.Stdout(), []storageCheckReport{rep}); err != nil {
			return err
		}
	} else {
		printStorageReport(c.Stdout(), rep)
	}
	if !rep.OK {
		return cli.Exit(1, fmt.Errorf("the storage check failed: fix the steps marked fail"))
	}
	return nil
}

func printStorageReport(w io.Writer, rep storageCheckReport) {
	fmt.Fprintf(w, "%s  bucket %s  prefix %s  endpoint %s", rep.Provider, rep.Bucket, rep.Prefix, rep.Endpoint)
	if rep.PublicEndpoint != nil {
		fmt.Fprintf(w, "  (links for %s)", *rep.PublicEndpoint)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range rep.Steps {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Level, s.Name, s.Detail)
	}
	tw.Flush()
	fmt.Fprintf(w, "signed SHA-256: %s; default encryption: %s; link host: %s\n", rep.ChecksumSHA256, rep.Encryption, rep.LinkHost)
	for _, r := range rep.CORS {
		fmt.Fprintf(w, "CORS: origins %v, methods %v\n", r.Origins, r.Methods)
	}
	if rep.OK {
		fmt.Fprintln(w, "ok")
	}
}
