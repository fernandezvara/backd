package jsonnum

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNumber(t *testing.T) {
	tests := []struct {
		in   string
		want any
	}{
		{"0", int64(0)},
		{"-42", int64(-42)},
		{"9223372036854775807", int64(9223372036854775807)},
		{"9223372036854775808", float64(9223372036854775808)},
		{"1.5", 1.5},
		{"2.0", int64(2)},
		{"1e3", int64(1000)},
		{"-0.0", int64(0)},
		{"1e18", int64(1000000000000000000)},
		{"9.2233720368547758e18", float64(9223372036854775808)},
		{"1e19", 1e19},
		{"2.5e0", 2.5},
		{"-9223372036854775808", int64(-9223372036854775808)},
		{"-9.223372036854775808e18", int64(-9223372036854775808)},
	}
	for _, tt := range tests {
		if got := Number(json.Number(tt.in)); got != tt.want {
			t.Errorf("Number(%s) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeNested(t *testing.T) {
	in := map[string]any{
		"a": json.Number("1"),
		"b": []any{json.Number("2.5"), map[string]any{"c": json.Number("3")}},
		"d": "text",
	}
	want := map[string]any{
		"a": int64(1),
		"b": []any{2.5, map[string]any{"c": int64(3)}},
		"d": "text",
	}
	if got := Normalize(in); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}
