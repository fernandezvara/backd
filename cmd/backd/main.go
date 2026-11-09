// Command backd is backd's command line, for developers and administrators: the project
// tools (templates, checks, types) and a running backd's admin API. The server is
// backd-server.
package main

import (
	"os"

	"github.com/fernandezvara/backd/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}
