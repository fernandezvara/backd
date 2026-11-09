package project

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fernandezvara/backd/internal/cli/clicore"

	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/cmdutil"
)

// exportDecrypt handles `backd export-decrypt`: a copy of a package with some encrypted fields
// opened, made offline.
func ExportDecrypt(c *kli.CommandContext) error {
	stderr := c.Stderr()
	keys, err := clicore.ParseFieldKeys(cmdutil.Str(c, "field"), c.Getenv, stderr)
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	if len(keys) == 0 {
		return cmdutil.UsageErr(errors.New("--field names no key"))
	}
	dir, cleanup, err := clicore.OpenPackage(cmdutil.Str(c, "from"))
	if err != nil {
		return err
	}
	defer cleanup()
	m, err := clicore.VerifyPackage(dir, stderr)
	if err != nil {
		return err
	}
	out := strings.TrimRight(cmdutil.Str(c, "out"), "/")
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty: choose another --out", out)
	}
	data, err := clicore.PackageKeys(dir)
	if err != nil {
		return err
	}
	dec, err := clicore.NewDecryptor(m, keys, data)
	if err != nil {
		return cmdutil.UsageErr(err)
	}
	if !dec.Any() {
		return fmt.Errorf("the package holds no encrypted field as ciphertext that these keys open")
	}
	if err := clicore.CopyPackageTree(dir, out); err != nil {
		return err
	}
	n, err := clicore.DecryptPackage(out, m, dec)
	if err != nil {
		_ = os.RemoveAll(out)
		return err
	}
	if err := m.Write(out); err != nil {
		return err
	}
	if left := clicore.CiphertextFields(m); len(left) > 0 {
		fmt.Fprintf(stderr, "still encrypted (no key given): %s\n", strings.Join(left, ", "))
	}
	fmt.Fprintf(stderr, "%d encrypted fields are in plaintext in %s: keep it as carefully as the keys\n", n, out)
	fmt.Fprintln(c.Stdout(), out)
	return nil
}
