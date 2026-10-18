package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMiddlewareLabelsByPatternAndCode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := Middleware(mux)

	for _, path := range []string{"/items/1", "/items/2", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	// both item paths share one series (pattern, not path), with the status the handler wrote
	if got := testutil.ToFloat64(requests.WithLabelValues("GET /items/{id}", "GET", "418")); got != 2 {
		t.Fatalf("pattern series = %v, want 2", got)
	}
	if got := testutil.ToFloat64(requests.WithLabelValues("unmatched", "GET", "404")); got != 1 {
		t.Fatalf("unmatched series = %v, want 1", got)
	}
	if got := testutil.ToFloat64(inFlight); got != 0 {
		t.Fatalf("in-flight after requests = %v, want 0", got)
	}
}
