package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"inference-gateway/internal/metrics"
)

type prompt struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Think  bool   `json:"think"`
}

// shortcut: no streaming, add "stream": true + flushing when you need token-by-token output
func Completion(ollamaURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in prompt
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Model == "" || in.Prompt == "" {
			http.Error(w, "need JSON with model and prompt", http.StatusBadRequest)
			return
		}

		body, err := json.Marshal(map[string]any{"model": in.Model, "prompt": in.Prompt, "stream": false, "think": in.Think, "keep_alive": "30m",
			// 8k context keeps qwen3:14b fully in GPU memory; the 40k default spills to CPU (~4x slower)
			"options": map[string]any{"num_ctx": 8192},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// r.Context() cancels the Ollama call if the client disconnects or the server shuts down
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ollamaURL+"/api/generate", bytes.NewReader(body))
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
			Model    string `json:"model"`
			Response string `json:"response"`
			metrics.LLMStats
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			http.Error(w, "bad response from ollama", http.StatusBadGateway)
			return
		}
		metrics.ObserveLLM(out.Model, out.LLMStats)
		json.NewEncoder(w).Encode(struct {
			Model    string `json:"model"`
			Response string `json:"response"`
		}{out.Model, out.Response})
	}
}
