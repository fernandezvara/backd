package storage

import (
	"reflect"
	"testing"
)

func TestSimplify(t *testing.T) {
	a := Condition{Field: "a", Op: OpEq, Value: int64(1)}
	b := Condition{Field: "b", Op: OpEq, Value: int64(2)}
	tests := []struct {
		in, want Filter
	}{
		{And{a, Const(true)}, a},
		{And{a, Const(false)}, Const(false)},
		{And{}, Const(true)},
		{Or{a, Const(true)}, Const(true)},
		{Or{a, Const(false), b}, Or{a, b}},
		{Or{}, Const(false)},
		{Not{Const(true)}, Const(false)},
		{Not{And{a, Const(true)}}, Not{a}},
		{And{Or{Const(false), a}, Not{Const(false)}}, a},
	}
	for _, tt := range tests {
		if got := Simplify(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Simplify(%#v) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}
