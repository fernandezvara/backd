package auth

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// DefaultHashConcurrency is how many password hashes may run at once when
// PASSWORD_HASH_CONCURRENCY isn't set: no more than the Go scheduler's
// processors (GOMAXPROCS, which follows the container's CPU limit), and no
// more than fit in half the memory limit (the cgroup's, or GOMEMLIMIT), so
// a flood of logins can't get the process killed for running out of
// memory. runtime.NumCPU is not used: in a container it reports the
// host's CPUs.
func DefaultHashConcurrency(p Argon2Params) int {
	return hashConcurrency(runtime.GOMAXPROCS(0), memoryLimit(), int64(p.Memory)*1024)
}

// hashConcurrency caps procs by memory: at most half of limit (0 when
// unknown) divided by perHash bytes, and at least 1.
func hashConcurrency(procs int, limit, perHash int64) int {
	n := max(procs, 1)
	if limit > 0 && perHash > 0 {
		n = min(n, int(limit/2/perHash))
	}
	return max(n, 1)
}

// memoryLimit returns the smallest known memory limit in bytes, or 0.
func memoryLimit() int64 {
	var limit int64
	consider := func(v int64) {
		if v > 0 && (limit == 0 || v < limit) {
			limit = v
		}
	}
	consider(cgroupLimit(os.ReadFile))
	if v := debug.SetMemoryLimit(-1); v != math.MaxInt64 { // GOMEMLIMIT
		consider(v)
	}
	return limit
}

// cgroupLimit reads the container's memory limit: cgroup v2, then v1.
// It returns 0 when there is none.
func cgroupLimit(read func(string) ([]byte, error)) int64 {
	for _, f := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		data, err := read(f)
		if err != nil {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		// "max" (v2) and huge values (v1's "unlimited") mean no limit.
		if err != nil || v <= 0 || v >= 1<<60 {
			return 0
		}
		return v
	}
	return 0
}
