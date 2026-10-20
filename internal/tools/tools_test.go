package tools

import (
	"testing"
	"time"
)

func TestDefinitionsMatchOllamaShape(t *testing.T) {
	defs := Definitions()
	if len(defs) != len(Registry) {
		t.Fatalf("got %d definitions, want %d (one per registered tool)", len(defs), len(Registry))
	}
	for _, d := range defs {
		if d["type"] != "function" {
			t.Errorf(`type = %v, want "function"`, d["type"])
		}
		fn, ok := d["function"].(map[string]any)
		if !ok {
			t.Fatalf(`"function" is %T, want map[string]any`, d["function"])
		}
		name, _ := fn["name"].(string)
		if _, registered := Registry[name]; !registered {
			t.Errorf("definition name %q is not in the Registry", name)
		}
		if fn["description"] == "" || fn["parameters"] == nil {
			t.Errorf("%q: description and parameters must be set; the model relies on them", name)
		}
	}
}

func TestRunGetCurrentTime(t *testing.T) {
	out, err := Run("get_current_time", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, out); err != nil {
		t.Errorf("output %q is not an RFC 3339 time: %v", out, err)
	}
}

func TestRunUnknownToolIsAnError(t *testing.T) {
	// the name comes from the model, so a made-up name must fail cleanly, not panic
	if _, err := Run("delete_everything", nil); err == nil {
		t.Fatal("expected an error for an unregistered tool")
	}
}
