package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveToolCountsByOutcome(t *testing.T) {
	okBefore := testutil.ToFloat64(toolCalls.WithLabelValues("calculator", "ok"))
	errBefore := testutil.ToFloat64(toolCalls.WithLabelValues("calculator", "error"))

	ObserveTool("calculator", time.Millisecond, nil)
	ObserveTool("calculator", time.Millisecond, nil)
	ObserveTool("calculator", time.Millisecond, errors.New("division by zero"))

	if got := testutil.ToFloat64(toolCalls.WithLabelValues("calculator", "ok")) - okBefore; got != 2 {
		t.Errorf("ok calls = %v, want 2", got)
	}
	if got := testutil.ToFloat64(toolCalls.WithLabelValues("calculator", "error")) - errBefore; got != 1 {
		t.Errorf("error calls = %v, want 1", got)
	}
}

func TestObserveRounds(t *testing.T) {
	before := testutil.CollectAndCount(toolAgentRounds) // one series, no labels
	ObserveRounds(0)
	ObserveRounds(2)
	if before != 1 || testutil.CollectAndCount(toolAgentRounds) != 1 {
		t.Fatalf("rounds should be a single unlabelled histogram")
	}
}
