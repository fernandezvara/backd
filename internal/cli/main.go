// Package cli is backd's command tree: every command, run in-process by
// Run so tests drive it like the binary.
package cli

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/server"
	"github.com/fernandezvara/backd/internal/templates"
)

// Run executes one backd command line and returns the process exit code.
func Run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		args = []string{"version"}
	}
	if isGlobalHelp(args) {
		fmt.Fprint(stdout, groupedHelp(getenv))
		return 0
	}
	return cmdutil.Execute(newCLI(getenv, stdin, stdout, stderr), "backd", args, stderr)
}

// role runs one of backd's server roles with its flags (see server.Run).
func role(name string) kli.CommandFunc {
	return cmdutil.Act("backd "+name, func(c *kli.CommandContext) error {
		o := server.Options{WithWorker: name == "serve" && cmdutil.Flag(c, "with-worker")}
		if name == "provision" {
			o.DisablePreImages, o.Yes = cmdutil.Flag(c, "disable-preimages"), cmdutil.Flag(c, "yes")
		}
		return server.Run(context.Background(), name, c.Getenv, c.Stderr(), o)
	})
}

// templateRealm, templateDatabase, templateFunction and templateProject
// handle `backd template ...`: they write files under CONFIG_DIR (the
// project template, a whole directory) and list what they created.
func templateRealm(c *kli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.Realm(dir, cmdutil.Str(c, "realm"), cmdutil.Flag(c, "sample"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateDatabase(c *kli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	realm, database := cmdutil.Str(c, "realm"), cmdutil.Str(c, "database")
	files, err := templates.Database(dir, realm, database, cmdutil.Flag(c, "sample"))
	if err == nil {
		fmt.Fprintf(c.Stdout(), "database  %s/%s/\n", realm, database)
	}
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateFunction(c *kli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.Function(dir, cmdutil.Str(c, "realm"), cmdutil.Str(c, "database"), cmdutil.Str(c, "name"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateEmailCapture(c *kli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.EmailCapture(dir, cmdutil.Str(c, "realm"), cmdutil.Str(c, "database"))
	printTemplateFiles(c.Stdout(), files)
	if err == nil {
		fmt.Fprintf(c.Stdout(), "\nnext: set `email.function: %s/email-capture` in %s/realm.yaml (see the file's comments), then run backd with BACKD_DEV=true\n", cmdutil.Str(c, "database"), cmdutil.Str(c, "realm"))
	}
	return err
}

func templateCollectionPolicy(c *kli.CommandContext) error {
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	files, err := templates.CollectionPolicy(dir, cmdutil.Str(c, "realm"), cmdutil.Str(c, "database"), cmdutil.Str(c, "collection"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func templateProject(c *kli.CommandContext) error {
	dir := cmdutil.Str(c, "dir")
	files, err := templates.Project(dir, cmdutil.Str(c, "realm"))
	printTemplateFiles(c.Stdout(), files)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout(), "\nnext: cd %s && cat README.md\n", dir)
	return nil
}

func printTemplateFiles(stdout io.Writer, files []templates.File) {
	for _, f := range files {
		if f.Created {
			fmt.Fprintf(stdout, "created   %s\n", f.Path)
		} else {
			fmt.Fprintf(stdout, "exists    %s (left unchanged)\n", f.Path)
		}
	}
}

// listDatabases handles `backd databases [--collections]`: it prints the
// MongoDB databases the config uses, data databases and realm system
// databases, sorted; with --collections, every collection as
// <database>.<collection>. It needs only CONFIG_DIR, so deploy scripts can
// grant a least-privilege user access to exactly these.
func listDatabases(c *kli.CommandContext) error {
	collections := cmdutil.Flag(c, "collections")
	dir, err := configDir(c)
	if err != nil {
		return err
	}
	reg, err := registry.Load(dir)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	var names []string
	add := func(db string, colls []string) {
		if !collections {
			names = append(names, db)
			return
		}
		for _, c := range colls {
			names = append(names, db+"."+c)
		}
	}
	for _, db := range reg.Databases() {
		var colls []string
		for _, c := range db.SortedCollections() {
			colls = append(colls, c.Name)
		}
		add(db.MongoName, colls)
	}
	for _, rl := range reg.SortedRealms() {
		if rl.Settings.AuthEnabled {
			add(mongodb.SystemDatabaseName(rl.Name), mongodb.SystemCollectionNames())
		}
	}
	// Where provisioning records the config fingerprint (read by verify).
	add(mongodb.DeploymentDatabase, []string{mongodb.DeploymentCollection})
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintln(c.Stdout(), n)
	}
	return nil
}
