package api

import (
	"fmt"
	"io"
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

func TestChatStreamRunsToolsBetweenRounds(t *testing.T) {
	t.Run("tool call on the done line", func(t *testing.T) {
		testToolRounds(t, `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_current_time","arguments":{}}}]},"done":true,"done_reason":"stop"}`)
	})
	t.Run("tool call on its own line before done", func(t *testing.T) { // Ollama 0.40 with qwen3:8b
		testToolRounds(t,
			`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"get_current_time","arguments":{}}}]},"done":false}`+"\n"+
				`{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
	})
}

// testToolRounds: round 1 streams some text and then ends with toolLines; round 2 answers.
func testToolRounds(t *testing.T, toolLines string) {
	var round2 string // the request body Ollama receives in round 2
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 { // round 1: some text, then the model orders a tool on the done line
			fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","content":"Let me check. "},"done":false}`)
			fmt.Fprintln(w, toolLines)
			return
		}
		b, _ := io.ReadAll(r.Body)
		round2 = string(b)
		fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","content":"It is noon."},"done":false}`)
		fmt.Fprintln(w, `{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
	}))
	defer srv.Close()

	got := streamRequest(t, srv.URL)

	if calls != 2 {
		t.Fatalf("Ollama called %d times, want 2 (one per round)", calls)
	}
	if !strings.Contains(got, `"content":"Let me check. "`) || !strings.Contains(got, `"content":"It is noon."`) {
		t.Errorf("client should get the text of both rounds in one stream:\n%s", got)
	}
	if strings.Count(got, "[DONE]") != 1 || !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Errorf("[DONE] must come exactly once, at the very end:\n%s", got)
	}
	if strings.Contains(got, "tool_calls") {
		t.Errorf("tool calls are internal and must not be streamed to the client:\n%s", got)
	}
	// round 2 must carry the model's own message (with its streamed text) and the tool result
	if !strings.Contains(round2, `"content":"Let me check. "`) || !strings.Contains(round2, `"role":"tool"`) ||
		!strings.Contains(round2, `"tool_name":"get_current_time"`) {
		t.Errorf("round 2 request is missing the assistant message or tool result:\n%s", round2)
	}
}
