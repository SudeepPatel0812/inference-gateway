package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"inference-gateway/internal/metrics"
)

type chatRequest struct {
	Messages []message `json:"messages"`
	Model    string    `json:"model"`
	Think    bool      `json:"think"`
	Stream   bool      `json:"stream"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// choice and chatResponse follow OpenAI's response shape so existing clients and UIs work unchanged
type choice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type chatResponse struct {
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
}

func Chat(ollamaURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in chatRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Model == "" || len(in.Messages) == 0 {
			http.Error(w, "need JSON with model and messages", http.StatusBadRequest)
			return
		}

		body, err := json.Marshal(map[string]any{
			"model":      in.Model,
			"messages":   in.Messages,
			"stream":     in.Stream,
			"think":      in.Think,
			"keep_alive": "30m",
			"options":    map[string]any{"num_ctx": 8192},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ollamaURL+"/api/chat", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "ollama unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/json")

		// pass Ollama errors (e.g. unknown model) through unchanged
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}

		if in.Stream {
			streamChat(w, resp.Body)
			return
		}

		var out struct {
			Model      string  `json:"model"`
			Message    message `json:"message"`
			DoneReason string  `json:"done_reason"`
			metrics.LLMStats
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			http.Error(w, "bad response from ollama", http.StatusBadGateway)
			return
		}
		metrics.ObserveLLM(out.Model, out.LLMStats)
		json.NewEncoder(w).Encode(chatResponse{
			Model:   out.Model,
			Choices: []choice{{Message: out.Message, FinishReason: out.DoneReason}},
		})
	}
}

// ---- streaming ----

// ollamaChunk is one line of Ollama's stream.
type ollamaChunk struct {
	Model            string  `json:"model"`
	Message          message `json:"message"`     // the new piece of text
	Done             bool    `json:"done"`        // true on the last line
	DoneReason       string  `json:"done_reason"` // e.g. "stop", on the last line
	Error            string  `json:"error"`       // set if Ollama failed mid-stream
	metrics.LLMStats         // token counts and timings, on the last line
}

// streamChunk is one event we send to the client, in OpenAI's streaming shape.
type streamChunk struct {
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Index        int     `json:"index"`
	Delta        message `json:"delta"`         // only the new piece of text
	FinishReason *string `json:"finish_reason"` // nil → null until the last chunk, like OpenAI
}

// streamChat reads Ollama's stream line by line and forwards each piece to the client
// as a Server-Sent Event. The first write is the first token, so TTFB = time to first token.
func streamChat(w http.ResponseWriter, body io.Reader) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(body)
	for scanner.Scan() { // waits for each new line from Ollama
		var chunk ollamaChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			continue // skip a broken line
		}

		// 1. Ollama failed: the 200 is already sent, so report the error inside the stream
		if chunk.Error != "" {
			writeEvent(w, flusher, map[string]any{"error": map[string]string{"message": chunk.Error}})
			return
		}

		// 2. Last line: record metrics, send the finish reason, then [DONE]
		if chunk.Done {
			metrics.ObserveLLM(chunk.Model, chunk.LLMStats)
			reason := chunk.DoneReason
			writeEvent(w, flusher, streamChunk{
				Model:   chunk.Model,
				Choices: []streamChoice{{Delta: message{Role: "assistant"}, FinishReason: &reason}},
			})
			w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
			return
		}

		// 3. Normal line: forward the new piece of text
		err := writeEvent(w, flusher, streamChunk{
			Model:   chunk.Model,
			Choices: []streamChoice{{Delta: chunk.Message}},
		})
		if err != nil {
			return // client disconnected; r.Context() also cancels the Ollama call
		}
	}

	// the loop ended without a done line: Ollama crashed or the connection dropped
	if err := scanner.Err(); err != nil {
		writeEvent(w, flusher, map[string]any{"error": map[string]string{"message": "stream interrupted: " + err.Error()}})
	}
}

// writeEvent sends one SSE event ("data: <json>" + a blank line) and flushes it to the client now.
func writeEvent(w http.ResponseWriter, flusher http.Flusher, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
		return err
	}
	flusher.Flush() // without this, Go buffers and the client gets everything at the end
	return nil
}
