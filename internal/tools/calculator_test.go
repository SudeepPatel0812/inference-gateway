package tools

import "testing"

func TestCalculator(t *testing.T) {
	ok := []struct {
		a, b float64
		op   string
		want string
	}{
		{17, 23, "multiply", "391"},
		{1234, 5678, "multiply", "7006652"},
		{10, 4, "divide", "2.5"},
		{2, 3, "subtract", "-1"},
		{0.1, 0.2, "add", "0.30000000000000004"}, // float64, not decimal: fine for a demo tool
	}
	for _, c := range ok {
		got, err := Run("calculator", map[string]any{"a": c.a, "b": c.b, "op": c.op})
		if err != nil || got != c.want {
			t.Errorf("%v %s %v = %q, %v; want %q", c.a, c.op, c.b, got, err, c.want)
		}
	}

	bad := map[string]map[string]any{
		"divide by zero": {"a": 1.0, "b": 0.0, "op": "divide"},
		"unknown op":     {"a": 1.0, "b": 2.0, "op": "power"},
		"missing b":      {"a": 1.0, "op": "add"},
		"string number":  {"a": "17", "b": 2.0, "op": "add"},
		"int not float":  {"a": 17, "b": 2.0, "op": "add"}, // JSON never produces int; reject anything else
	}
	for name, args := range bad {
		if _, err := Run("calculator", args); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
