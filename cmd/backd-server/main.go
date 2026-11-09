// Command backd-server runs backd's server roles (serve, worker, executor, egress, provision)
// and the tools that work on MongoDB or run Deno directly. The developer's and administrator's
// commands are the backd CLI's.
package main

import (
	"os"

	"github.com/fernandezvara/backd/internal/servercli"
)

func main() {
	os.Exit(servercli.Run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}
