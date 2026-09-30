// Package jsonnum converts json.Number values into concrete Go numbers.
//
// Integral numbers that fit in 64 bits become int64, however they are
// written (3, 3.0, 3e0); all others become float64. JSON Schema counts 3.0
// as an integer, so storing it as int64 keeps "integer" fields integer-typed
// in MongoDB. Integers beyond 64 bits stay float64 (and may lose precision).
package jsonnum

import (
	"encoding/json"
	"math"
	"strconv"
)

// Normalize returns v with every json.Number (at any depth inside maps and
// slices) replaced by an int64 or float64. Maps and slices are modified in place.
func Normalize(v any) any {
	switch t := v.(type) {
	case json.Number:
		return Number(t)
	case map[string]any:
		for k, e := range t {
			t[k] = Normalize(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = Normalize(e)
		}
		return t
	}
	return v
}

// Number converts one json.Number. Integral values outside the int64 range,
// and values with a fraction or exponent, become float64.
func Number(n json.Number) any {
	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return i
	}
	f, _ := strconv.ParseFloat(n.String(), 64)
	// -2^63 <= f < 2^63: both bounds are exact in float64.
	if f == math.Trunc(f) && f >= math.MinInt64 && f < math.MaxInt64 {
		return int64(f)
	}
	return f
}
