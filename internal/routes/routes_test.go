package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"inference-gateway/internal/config"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesUIWithoutShadowingAPI(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>Gateway Chat</title>"), 0o644)
	mux := Routes(config.Config{StaticDir: dir})

	if rec := get(t, mux, "/"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Gateway Chat") {
		t.Errorf("GET / = %d %q, want the UI", rec.Code, rec.Body.String())
	}
	if rec := get(t, mux, "/health"); !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Errorf("GET /health was shadowed by the UI: %q", rec.Body.String())
	}
	if rec := get(t, mux, "/metrics"); !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Error("GET /metrics was shadowed by the UI")
	}
}

func TestNoUIWhenStaticDirUnset(t *testing.T) {
	if rec := get(t, Routes(config.Config{}), "/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET / without STATIC_DIR = %d, want 404 (API only)", rec.Code)
	}
}
