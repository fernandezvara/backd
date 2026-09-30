package auth

import (
	"errors"
	"testing"
)

func TestHashConcurrency(t *testing.T) {
	const mib = 1 << 20
	per := int64(DefaultArgon2Params.Memory) * 1024 // 64 MiB
	for _, tt := range []struct {
		name  string
		procs int
		limit int64
		want  int
	}{
		{"no memory limit: processors", 8, 0, 8},
		{"1 CPU, 512 MiB container", 2, 512 * mib, 2},
		{"32 CPUs but 512 MiB: memory wins", 32, 512 * mib, 4},
		{"4 CPUs, 256 MiB: memory wins", 4, 256 * mib, 2},
		{"tiny container still hashes one at a time", 4, 64 * mib, 1},
		{"zero processors reported", 0, 0, 1},
	} {
		if got := hashConcurrency(tt.procs, tt.limit, per); got != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestCgroupLimit(t *testing.T) {
	files := func(m map[string]string) func(string) ([]byte, error) {
		return func(f string) ([]byte, error) {
			if v, ok := m[f]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("missing")
		}
	}
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  int64
	}{
		{"v2 limit", map[string]string{"/sys/fs/cgroup/memory.max": "536870912\n"}, 536870912},
		{"v2 unlimited", map[string]string{"/sys/fs/cgroup/memory.max": "max\n"}, 0},
		{"v1 limit", map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "268435456"}, 268435456},
		{"v1 unlimited", map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712"}, 0},
		{"no cgroup files", nil, 0},
	} {
		if got := cgroupLimit(files(tt.files)); got != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestNewHasherDefaultIsBounded(t *testing.T) {
	h := NewHasher(0, DefaultArgon2Params)
	if want := DefaultHashConcurrency(DefaultArgon2Params); h.Concurrency() != want || want < 1 {
		t.Errorf("default concurrency %d, want %d (≥ 1)", h.Concurrency(), want)
	}
	if NewHasher(3, DefaultArgon2Params).Concurrency() != 3 {
		t.Error("explicit concurrency ignored")
	}
}
