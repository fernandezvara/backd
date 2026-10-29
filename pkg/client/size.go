package client

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// SizeFamily says which units a size accepts (and, for output, which it prints).
type SizeFamily int

const (
	// SizeBoth accepts decimal and binary units. It is the zero value and the default for input;
	// for output it prints binary.
	SizeBoth SizeFamily = iota
	// SizeDecimal accepts B, KB, MB, GB and TB: powers of 1000.
	SizeDecimal
	// SizeBinary accepts B, KiB, MiB, GiB and TiB: powers of 1024.
	SizeBinary
)

// Units lists the units the family accepts, as a message names them.
func (f SizeFamily) Units() string {
	switch f {
	case SizeDecimal:
		return "B, KB, MB, GB, TB"
	case SizeBinary:
		return "B, KiB, MiB, GiB, TiB"
	}
	return "B, KB, MB, GB, TB, KiB, MiB, GiB, TiB"
}

const maxSizeInput = 64

var sizePattern = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$`)

var (
	decimalUnits = []string{"B", "KB", "MB", "GB", "TB"}
	binaryUnits  = []string{"B", "KiB", "MiB", "GiB", "TiB"}
	sizeUnits    = map[string]int64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}
)

// ParseSize parses a size such as "128MB" (powers of 1000) or "1.5MiB" (powers of 1024). The unit
// is case-sensitive and follows the number directly. A decimal is fine when the result is a whole
// number of bytes. family limits the units; allowBytes also accepts a bare number of bytes
// ("1048576"). It follows the one size grammar of the server and the other clients.
func ParseSize(v string, family SizeFamily, allowBytes bool) (int64, error) {
	s := strings.TrimSpace(v)
	var m []string
	if len(s) <= maxSizeInput {
		m = sizePattern.FindStringSubmatch(s)
	}
	if m == nil || (m[3] == "" && (!allowBytes || m[2] != "")) {
		want := "a number and a unit (" + family.Units() + "), such as 128MiB"
		if family == SizeDecimal {
			want = "a number and a unit (" + family.Units() + "), such as 128MB"
		}
		if allowBytes {
			want = "a whole number of bytes or " + want
		}
		return 0, fmt.Errorf("invalid size %q: want %s", v, want)
	}
	unit, intPart, frac := m[3], m[1], m[2]
	if unit == "" {
		unit = "B"
	}
	binary := strings.HasSuffix(unit, "iB")
	if (family == SizeDecimal && binary) || (family == SizeBinary && !binary && unit != "B") {
		return 0, fmt.Errorf("invalid size %q: %s is not accepted here, use one of %s", v, unit, family.Units())
	}
	digits, _ := new(big.Int).SetString(intPart+frac, 10)
	digits.Mul(digits, big.NewInt(sizeUnits[unit]))
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(frac))), nil)
	q, r := new(big.Int).QuoRem(digits, scale, new(big.Int))
	if r.Sign() != 0 {
		return 0, fmt.Errorf("invalid size %q: not a whole number of bytes", v)
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("invalid size %q: too large", v)
	}
	return q.Int64(), nil
}

// FormatSize prints n for people: the largest unit whose value is at least 1, up to TB or TiB,
// with at most two decimals and no trailing zeros ("1.5 KiB", "5 MiB"). SizeBoth prints binary.
func FormatSize(n int64, family SizeFamily) string {
	base, units := 1024.0, binaryUnits
	if family == SizeDecimal {
		base, units = 1000.0, decimalUnits
	}
	if float64(n) < base {
		return strconv.FormatInt(n, 10) + " B"
	}
	v, i := float64(n), 0
	for ; i < len(units)-1; i++ {
		if math.Round(v*100)/100 < base {
			break
		}
		v /= base
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) + " " + units[i]
}
