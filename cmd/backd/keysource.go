package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readKeySource reads a key the operator names as key:<value>, env:<VAR> or file:<path>, the
// same way everywhere a key is given on the command line. A key given as a value ends up in
// shell history and process lists, which the docs warn about; a file wider than owner-only
// draws a warning on warn.
func readKeySource(spec string, getenv func(string) string, warn io.Writer) (string, error) {
	kind, value, ok := strings.Cut(spec, ":")
	if !ok || value == "" {
		return "", fmt.Errorf("want key:<value>, env:<VAR> or file:<path>, got %q", spec)
	}
	switch kind {
	case "key":
		return value, nil
	case "env":
		v := strings.TrimSpace(getenv(value))
		if v == "" {
			return "", fmt.Errorf("the environment variable %s is not set", value)
		}
		return v, nil
	case "file":
		st, err := os.Stat(value)
		if err != nil {
			return "", err
		}
		if st.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(warn, "warning: %s can be read by others (mode %o): restrict it to your user\n", value, st.Mode().Perm())
		}
		data, err := os.ReadFile(value)
		if err != nil {
			return "", err
		}
		v := strings.TrimSpace(string(data))
		if v == "" {
			return "", errors.New(value + " is empty")
		}
		return v, nil
	}
	return "", fmt.Errorf("unknown key source %q (want key, env or file)", kind)
}
