// Package cmdutil holds the helpers backd's command trees share: flag
// definitions and reads, the wrapper every command runs in, exit codes,
// the user's terminal and JSON output. It is built on kli and never touches
// the process environment or standard streams directly, so commands using it
// run in-process in tests.
package cmdutil

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fernandezvara/kli"
	"golang.org/x/term"
)

// Exit codes: 0 for success, ExitFailure for a command that ran and failed,
// ExitUsage for one invoked wrongly.
const (
	ExitFailure = 1
	ExitUsage   = kli.ExitUsage
)

// Execute runs args (without the program name) on cfg and returns the exit
// code: 0, ExitFailure, ExitUsage, or what the command chose. An error not
// yet reported is printed to stderr, prefixed with prog.
func Execute(cfg *kli.Config, prog string, args []string, stderr io.Writer) int {
	err := cfg.Execute(append([]string{prog}, args...))
	if err != nil && !kli.IsReported(err) && err.Error() != "" {
		msg := err.Error()
		if !strings.HasPrefix(msg, prog) {
			msg = prog + ": " + msg
		}
		fmt.Fprintln(stderr, msg)
	}
	return kli.ExitCode(err)
}

// Act wraps a command named name (the whole command line, program
// included: "backd template realm"): stray arguments are a usage error, and
// failures are prefixed with the name.
func Act(name string, fn kli.CommandFunc) kli.CommandFunc {
	return func(c *kli.CommandContext) error {
		if extra := c.Positional(); len(extra) > 0 {
			return kli.Exit(ExitUsage, fmt.Errorf("%s: unexpected arguments: %v (values are given as flags: see `%s --help`)", name, extra, name))
		}
		if err := fn(c); err != nil {
			if _, ok := errors.AsType[*kli.ExitError](err); ok {
				return err
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
}

// UsageErr marks err as a usage error (ExitUsage): the command was invoked
// wrongly, not one that ran and failed.
func UsageErr(err error) error { return kli.Exit(ExitUsage, err) }

// Failed marks err as a failure (ExitFailure) that Act passes on unchanged.
func Failed(err error) error { return kli.Exit(ExitFailure, err) }

// Required, Optional and Boolean define a command's flags.
func Required(cc *kli.CommandConfig, key, description string) {
	cc.Define(key).String().Flag(key).Required().Description(description)
}

func Optional(cc *kli.CommandConfig, key, description string) {
	cc.Define(key).String().Flag(key).Default("").Description(description)
}

func Boolean(cc *kli.CommandConfig, key, description string) {
	cc.Define(key).Bool().Flag(key).Default(false).Description(description)
}

// Str, Flag and Num read a command's flags.
func Str(c *kli.CommandContext, key string) string { return kli.MustGet[string](c, key) }
func Flag(c *kli.CommandContext, key string) bool  { return kli.MustGet[bool](c, key) }
func Num(c *kli.CommandContext, key string) int64  { return kli.MustGet[int64](c, key) }

// IO is the user's side of a command: its standard streams.
type IO struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// IOOf is the IO of a running command.
func IOOf(c *kli.CommandContext) IO {
	return IO{Stdin: c.Stdin(), Stdout: c.Stdout(), Stderr: c.Stderr()}
}

// IsTerminal tells whether r is a terminal.
func IsTerminal(r any) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// ReadLine reads one line the user types (a code): from the terminal with a
// prompt, or the first line of standard input when it isn't one.
func ReadLine(uio IO, prompt string) (string, error) {
	if IsTerminal(uio.Stdin) {
		fmt.Fprintf(uio.Stderr, "%s: ", prompt)
	}
	line, err := bufio.NewReader(uio.Stdin).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("no code given")
	}
	return strings.TrimSpace(line), nil
}

// ReadSecret reads a password from the terminal without echo, twice when
// confirm is set; otherwise it reads the first line of stdin.
func ReadSecret(uio IO, confirm bool, prompt string) (string, error) {
	if f, ok := uio.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(uio.Stderr, "%s: ", prompt)
		p1, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(uio.Stderr)
		if err != nil {
			return "", err
		}
		if !confirm {
			return string(p1), nil
		}
		fmt.Fprint(uio.Stderr, "Repeat password: ")
		p2, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(uio.Stderr)
		if err != nil {
			return "", err
		}
		if string(p1) != string(p2) {
			return "", errors.New("passwords don't match")
		}
		return string(p1), nil
	}
	line, err := bufio.NewReader(uio.Stdin).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("no password on standard input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ReadSecretValue reads the value of a secret: a hidden prompt on a
// terminal, and all of standard input otherwise, so a file (a service
// account's JSON, a .p8 key) can be piped in. Only the line ending at the
// very end is dropped.
func ReadSecretValue(uio IO, prompt string) (string, error) {
	if IsTerminal(uio.Stdin) {
		return ReadSecret(uio, false, prompt)
	}
	data, err := io.ReadAll(io.LimitReader(uio.Stdin, 1<<20))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("no secret on standard input")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// PrintJSON writes v to w as one line of JSON.
func PrintJSON(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

// PrintJSONLines writes each item to w as a line of JSON.
func PrintJSONLines[T any](w io.Writer, items []T) error {
	enc := json.NewEncoder(w)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	return nil
}

// ConfigDir is CONFIG_DIR, required by commands that read the config.
func ConfigDir(c *kli.CommandContext) (string, error) {
	dir := c.Getenv("CONFIG_DIR")
	if dir == "" {
		return "", errors.New("CONFIG_DIR is required")
	}
	return dir, nil
}
