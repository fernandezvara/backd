package project

import (
	"fmt"
	"io"
	"slices"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
	"github.com/fernandezvara/backd/internal/dbnames"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/templates"
)

// templateRealm, templateDatabase, templateFunction and templateProject
// handle `backd template ...`: they write files under CONFIG_DIR (the
// project template, a whole directory) and list what they created.
func TemplateRealm(c *kli.CommandContext) error {
	dir, err := cmdutil.ConfigDir(c)
	if err != nil {
		return err
	}
	files, err := templates.Realm(dir, cmdutil.Str(c, "realm"), cmdutil.Flag(c, "sample"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func TemplateDatabase(c *kli.CommandContext) error {
	dir, err := cmdutil.ConfigDir(c)
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

func TemplateFunction(c *kli.CommandContext) error {
	dir, err := cmdutil.ConfigDir(c)
	if err != nil {
		return err
	}
	files, err := templates.Function(dir, cmdutil.Str(c, "realm"), cmdutil.Str(c, "database"), cmdutil.Str(c, "name"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func TemplateEmailCapture(c *kli.CommandContext) error {
	dir, err := cmdutil.ConfigDir(c)
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

func TemplateCollectionPolicy(c *kli.CommandContext) error {
	dir, err := cmdutil.ConfigDir(c)
	if err != nil {
		return err
	}
	files, err := templates.CollectionPolicy(dir, cmdutil.Str(c, "realm"), cmdutil.Str(c, "database"), cmdutil.Str(c, "collection"))
	printTemplateFiles(c.Stdout(), files)
	return err
}

func TemplateProject(c *kli.CommandContext) error {
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
func ListDatabases(c *kli.CommandContext) error {
	collections := cmdutil.Flag(c, "collections")
	dir, err := cmdutil.ConfigDir(c)
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
			add(dbnames.SystemDatabase(rl.Name), dbnames.SystemCollections())
		}
	}
	// Where provisioning records the config fingerprint (read by verify).
	add(dbnames.DeploymentDatabase, []string{dbnames.DeploymentCollection})
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintln(c.Stdout(), n)
	}
	return nil
}
