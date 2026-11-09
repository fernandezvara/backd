package clicore

import (
	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// remote adds the flags every command that talks to a running backd has.
func Remote(cc *kli.CommandConfig) {
	cmdutil.Required(cc, "realm", "the realm")
	cmdutil.Optional(cc, "url", "the server (default: BACKD_URL, else the last one logged in to)")
}

const AdminAPINote = "Uses the realm's admin API on a running backd, as the session stored by\n`backd login` or with the admin API key in BACKD_API_KEY."
