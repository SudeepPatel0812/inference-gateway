package tools

import (
	"strings"
	"testing"
	"time"
)

func TestTimeInTimezone(t *testing.T) {
	got, err := Run("get_current_time", map[string]any{"timezone": "Asia/Kolkata"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil || !strings.HasSuffix(got, "+05:30") {
		t.Errorf("got %q, want an RFC 3339 time with offset +05:30", got)
	}

	if got, _ := Run("get_current_time", nil); !strings.HasSuffix(got, "Z") {
		t.Errorf("default should be UTC, got %q", got)
	}

	if _, err := Run("get_current_time", map[string]any{"timezone": "Mars/Olympus"}); err == nil {
		t.Error("expected an error for an unknown timezone")
	}
}
