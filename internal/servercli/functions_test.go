package servercli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunExecutorArguments(t *testing.T) {
	for _, tt := range []struct {
		args []string
		env  map[string]string
		code int
		want string
	}{
		{[]string{"executor", "help"}, nil, 0, "Usage: backd-server executor"},
		{[]string{"executor", "extra"}, nil, 2, "unexpected arguments"},
		{[]string{"executor"}, map[string]string{"DENO": "/nonexistent/deno"}, 1, "can't run /nonexistent/deno"},
		{[]string{"executor"}, map[string]string{"EXECUTOR_MAX_PROCESSES": "0"}, 1, "EXECUTOR_MAX_PROCESSES must be a positive integer"},
		{[]string{"executor"}, map[string]string{"LOG_LEVEL": "loud"}, 1, "LOG_LEVEL must be"},
	} {
		var out bytes.Buffer
		code := Run(tt.args, func(k string) string { return tt.env[k] }, nil, &out, &out)
		if code != tt.code || !strings.Contains(out.String(), tt.want) {
			t.Errorf("%v: %d %q, want %d %q", tt.args, code, out.String(), tt.code, tt.want)
		}
	}
}
