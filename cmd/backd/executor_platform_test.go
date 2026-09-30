package main

import (
	"strings"
	"testing"
)

func TestExecutorRefusesUnsupportedSystems(t *testing.T) {
	old := executorSupported
	executorSupported = false
	defer func() { executorSupported = old }()
	err := serveExecutor(func(string) string { return "" }, nil)
	if err == nil || !strings.Contains(err.Error(), "only on Linux") || !strings.Contains(err.Error(), "container image") {
		t.Fatalf("got %v, want the Linux-only message", err)
	}
}
