//go:build !linux

package executor

import (
	"os"
	"syscall"
	"time"
)

// Supported is false here: New refuses to run, since nothing below can
// enforce a function's limits. The package compiles so the rest of backd
// (serve, worker, egress, the command line) still builds for this system.
const Supported = false

func processAttr() *syscall.SysProcAttr              { return nil }
func limitProcess(int, uint64)                       {}
func killGroup(int)                                  {}
func cpuUsed(*os.ProcessState) (time.Duration, bool) { return 0, false }
func rssOf(int) int64                                { return 0 }
