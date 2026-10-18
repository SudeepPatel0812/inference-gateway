package api

import (
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

// shortcut: no streaming, add "stream": true + flushing when you need token-by-token output
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
			"stream":     false,
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
