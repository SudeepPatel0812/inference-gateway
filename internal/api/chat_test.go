package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOllama answers /api/chat with the given NDJSON lines, like Ollama does with "stream": true.
func fakeOllama(lines ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}))
}

func streamRequest(t *testing.T, ollamaURL string) string {
	t.Helper()
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	Chat(ollamaURL)(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return rec.Body.String()
}

func TestChatStreamsSSEChunks(t *testing.T) {
	srv := fakeOllama(
		`{"model":"m","message":{"role":"assistant","content":"Hel"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"lo"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","eval_count":2}`,
	)
	defer srv.Close()

	got := streamRequest(t, srv.URL)

	for _, want := range []string{
		`"delta":{"role":"assistant","content":"Hel"},"finish_reason":null`,
		`"delta":{"role":"assistant","content":"lo"}`,
		`"finish_reason":"stop"`,
		"data: [DONE]\n\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stream missing %q\ngot:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Errorf("stream must end with [DONE]\ngot:\n%s", got)
	}
}

func TestChatStreamReportsMidStreamError(t *testing.T) {
	srv := fakeOllama(
		`{"model":"m","message":{"role":"assistant","content":"Hel"},"done":false}`,
		`{"error":"model runner crashed"}`,
	)
	defer srv.Close()

	got := streamRequest(t, srv.URL)

	if !strings.Contains(got, `data: {"error":{"message":"model runner crashed"}}`) {
		t.Errorf("missing in-band error event\ngot:\n%s", got)
	}
	if strings.Contains(got, "[DONE]") {
		t.Errorf("errored stream must not send [DONE]\ngot:\n%s", got)
	}
}
