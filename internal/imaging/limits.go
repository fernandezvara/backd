package imaging

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// DeriveConcurrency is how many images a worker can process at once: half the
// CPUs, and no more than fit in half the memory when each takes four bytes per
// pixel of the largest image allowed; at least one. cpus <= 0 means the host's;
// memory <= 0 means unknown, which leaves only the CPUs to count.
func DeriveConcurrency(cpus float64, memory, maxPixels int64) int {
	if cpus <= 0 {
		cpus = float64(runtime.NumCPU())
	}
	if maxPixels <= 0 {
		maxPixels = DefaultMaxPixels
	}
	n := int(cpus / 2)
	if memory > 0 {
		n = min(n, int(memory/2/(maxPixels*4)))
	}
	return max(n, 1)
}

// ContainerLimits reads the CPU and memory limits of the container the process
// runs in (cgroup v2, then v1): zero where there is no limit.
func ContainerLimits() (cpus float64, memory int64) {
	return containerLimits("/sys/fs/cgroup")
}

func containerLimits(root string) (cpus float64, memory int64) {
	read := func(name string) string {
		b, err := os.ReadFile(root + "/" + name)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	// v2: cpu.max is "max 100000" or "200000 100000"; memory.max is "max" or bytes.
	if f := strings.Fields(read("cpu.max")); len(f) == 2 && f[0] != "max" {
		q, err1 := strconv.ParseFloat(f[0], 64)
		p, err2 := strconv.ParseFloat(f[1], 64)
		if err1 == nil && err2 == nil && p > 0 {
			cpus = q / p
		}
	}
	if v := read("memory.max"); v != "" && v != "max" {
		memory, _ = strconv.ParseInt(v, 10, 64)
	}
	if cpus == 0 && memory == 0 { // v1
		q, err1 := strconv.ParseFloat(read("cpu/cpu.cfs_quota_us"), 64)
		p, err2 := strconv.ParseFloat(read("cpu/cpu.cfs_period_us"), 64)
		if err1 == nil && err2 == nil && q > 0 && p > 0 {
			cpus = q / p
		}
		if m, err := strconv.ParseInt(read("memory/memory.limit_in_bytes"), 10, 64); err == nil && m < 1<<60 {
			memory = m
		}
	}
	return cpus, memory
}
