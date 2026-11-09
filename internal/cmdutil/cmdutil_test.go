package cmdutil

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/fernandezvara/kli"
)

func tree(fn kli.CommandFunc) (*kli.Config, *bytes.Buffer) {
	var out bytes.Buffer
	cfg := kli.New().SetName("tool").SetEnv(func(string) string { return "" }).SetIO(strings.NewReader(""), &out, &out)
	cfg.Command("do").Func(Act("tool do", fn)).Config(func(cc *kli.CommandConfig) {
		Optional(cc, "name", "a name")
		Boolean(cc, "loud", "a switch")
	})
	return cfg, &out
}

func TestExecute(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		fn      kli.CommandFunc
		code    int
		wantErr string
	}{
		{"ok", []string{"do", "--name", "x", "--loud"}, func(c *kli.CommandContext) error {
			if Str(c, "name") != "x" || !Flag(c, "loud") {
				return errors.New("flags not read")
			}
			return nil
		}, 0, ""},
		{"positional", []string{"do", "extra"}, func(*kli.CommandContext) error { return nil }, ExitUsage,
			"tool do: unexpected arguments: [extra] (values are given as flags: see `tool do --help`)"},
		{"failure", []string{"do"}, func(*kli.CommandContext) error { return errors.New("boom") }, ExitFailure, "tool do: boom"},
		{"usage", []string{"do"}, func(*kli.CommandContext) error { return UsageErr(errors.New("tool do: bad")) }, ExitUsage, "tool do: bad"},
		{"failed", []string{"do"}, func(*kli.CommandContext) error { return Failed(errors.New("drift")) }, ExitFailure, "tool: drift"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, out := tree(tt.fn)
			var stderr bytes.Buffer
			if code := Execute(cfg, "tool", tt.args, &stderr); code != tt.code {
				t.Fatalf("code %d, want %d (stderr %q, out %q)", code, tt.code, stderr.String(), out.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != tt.wantErr {
				t.Errorf("stderr %q, want %q", got, tt.wantErr)
			}
		})
	}
}

func TestReadLineAndSecret(t *testing.T) {
	uio := IO{Stdin: strings.NewReader(" 123456 \nmore"), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if got, err := ReadLine(uio, "Code"); err != nil || got != "123456" {
		t.Errorf("ReadLine = %q, %v", got, err)
	}
	uio.Stdin = strings.NewReader("dev-p4ssw0rd!\r\n")
	if got, err := ReadSecret(uio, true, "Password"); err != nil || got != "dev-p4ssw0rd!" {
		t.Errorf("ReadSecret = %q, %v", got, err)
	}
	uio.Stdin = strings.NewReader("")
	if _, err := ReadSecret(uio, false, "Password"); err == nil {
		t.Error("ReadSecret on empty input: no error")
	}
	uio.Stdin = strings.NewReader("{\n \"k\": 1\n}\n")
	if got, err := ReadSecretValue(uio, "Value"); err != nil || got != "{\n \"k\": 1\n}" {
		t.Errorf("ReadSecretValue = %q, %v", got, err)
	}
}

func TestPrintJSON(t *testing.T) {
	var b bytes.Buffer
	if err := PrintJSONLines(&b, []map[string]int{{"a": 1}, {"b": 2}}); err != nil {
		t.Fatal(err)
	}
	if err := PrintJSON(&b, map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":1}\n{\"b\":2}\n{\"ok\":true}\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}
