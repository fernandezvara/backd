//go:build linux

package executor

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Supported reports whether this system can enforce a function's limits
// (CPU time, resident memory, one process group to kill). Only Linux can.
const Supported = true

// processAttr puts the function's process in its own group, so everything
// it starts can be killed together.
func processAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// limitProcess caps the process's CPU time (cpu seconds) and forbids core dumps.
func limitProcess(pid int, cpu uint64) {
	_ = unix.Prlimit(pid, unix.RLIMIT_CPU, &unix.Rlimit{Cur: cpu, Max: cpu + 1}, nil)
	_ = unix.Prlimit(pid, unix.RLIMIT_CORE, &unix.Rlimit{}, nil)
}

// killGroup kills the process group led by pid.
func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// cpuUsed is the CPU time an ended process used.
func cpuUsed(ps *os.ProcessState) (time.Duration, bool) {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0, false
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), true
}

// rssOf returns the resident memory of pid in bytes (0 if unknown).
func rssOf(pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				kb, _ := strconv.ParseInt(f[0], 10, 64)
				return kb << 10
			}
		}
	}
	return 0
}
