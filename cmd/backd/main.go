// Command backd is backd's command line and, until the split, its server.
package main

import (
	"os"

	"github.com/fernandezvara/backd/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}
