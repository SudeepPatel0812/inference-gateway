package tools

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8":         true,
		"1.1.1.1":         true,
		"127.0.0.1":       false, // loopback
		"10.0.0.5":        false, // private
		"172.18.0.3":      false, // Docker network (where Ollama, Prometheus, Grafana live)
		"192.168.1.10":    false, // home LAN
		"169.254.169.254": false, // cloud metadata endpoint
		"100.64.0.1":      false, // carrier-grade NAT
		"::1":             false,
		"0.0.0.0":         false,
	} {
		if got := isPublicIP(net.ParseIP(ip)); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestFetchBlocksInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("secret internal data"))
	}))
	defer srv.Close()

	// httptest listens on 127.0.0.1, exactly what an SSRF attempt would target
	_, err := Run("fetch_url", map[string]any{"url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected the loopback fetch to be blocked, got err=%v", err)
	}
}

func TestFetchRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "example.com", ""} {
		if _, err := Run("fetch_url", map[string]any{"url": u}); err == nil {
			t.Errorf("url %q: expected an error", u)
		}
	}
	if _, err := Run("fetch_url", map[string]any{}); err == nil {
		t.Error("missing url: expected an error")
	}
}

func TestFetchReturnsReadableText(t *testing.T) {
	allowIP = func(net.IP) bool { return true } // let this test reach the local httptest server
	defer func() { allowIP = isPublicIP }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><style>p{color:red}</style><script>alert(1)</script><h1>Hello</h1><p>Go   is fun</p></html>`))
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(strings.Repeat("a", fetchMaxChars+500)))
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte{0x89, 'P', 'N', 'G'})
		}
	}))
	defer srv.Close()

	got, err := Run("fetch_url", map[string]any{"url": srv.URL + "/page"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "HTTP 200\n\nHello Go is fun" {
		t.Errorf("html not reduced to text: %q", got)
	}

	got, _ = Run("fetch_url", map[string]any{"url": srv.URL + "/big"})
	if !strings.HasSuffix(got, "…[truncated]") {
		t.Errorf("long body should be truncated, got %d chars", len(got))
	}

	if _, err := Run("fetch_url", map[string]any{"url": srv.URL + "/image"}); err == nil {
		t.Error("binary content should be rejected")
	}
}
