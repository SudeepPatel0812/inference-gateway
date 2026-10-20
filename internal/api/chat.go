package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"inference-gateway/internal/metrics"
	"inference-gateway/internal/tools"
)

const maxToolRounds = 5

type chatRequest struct {
	Messages []message `json:"messages"`
	Model    string    `json:"model"`
	Think    bool      `json:"think"`
	Stream   bool      `json:"stream"`
}

type message struct {
	Role      string      `json:"role"`
	Content   string      `json:"content"`
	ToolCalls []toolsCall `json:"tool_calls,omitempty"`
	ToolName  string      `json:"tool_name,omitempty"`
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

type toolsCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

func Chat(ollamaURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in chatRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Model == "" || len(in.Messages) == 0 {
			http.Error(w, "need JSON with model and messages", http.StatusBadRequest)
			return
		}

		if !in.Stream {
			messages := in.Messages

			reply, err := ask(r.Context(), ollamaURL, in.Model, in.Think, messages)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}

			round := 0 // declared outside the loop so it can be recorded afterwards
			for ; round < maxToolRounds && len(reply.Message.ToolCalls) > 0; round++ {
				messages = append(messages, reply.Message)
				for _, order := range reply.Message.ToolCalls {
					result, runErr := tools.Run(order.Function.Name, order.Function.Arguments)
					if runErr != nil {
						result = "error: " + runErr.Error() // tell the model, so it can explain or recover
					}
					messages = append(messages, message{Role: "tool", ToolName: order.Function.Name, Content: result})
				}

				reply, err = ask(r.Context(), ollamaURL, in.Model, in.Think, messages)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
			}

			metrics.ObserveRounds(round) // before the give-up check, so stuck requests are counted too

			if len(reply.Message.ToolCalls) > 0 {
				http.Error(w, "model kept calling tools; gave up after max rounds", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(chatResponse{
				Model:   reply.Model,
				Choices: []choice{{Message: reply.Message, FinishReason: reply.DoneReason}},
			})

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

		streamChat(w, resp.Body)
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

// ollamaReply is one non-streaming answer from Ollama.
type ollamaReply struct {
	Model      string  `json:"model"`
	Message    message `json:"message"`
	DoneReason string  `json:"done_reason"`
	metrics.LLMStats
}

// ask sends the conversation to Ollama once (with the tool menu) and returns its reply.
func ask(ctx context.Context, ollamaURL, model string, think bool, messages []message) (ollamaReply, error) {
	var reply ollamaReply

	body, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   messages,
		"stream":     false,
		"think":      think,
		"keep_alive": "30m",
		"options":    map[string]any{"num_ctx": 8192},
		"tools":      tools.Definitions(), // the menu
	})
	if err != nil {
		return reply, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return reply, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply, fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return reply, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, msg)
	}

	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return reply, fmt.Errorf("bad response from ollama: %w", err)
	}
	metrics.ObserveLLM(reply.Model, reply.LLMStats)
	return reply, nil
}
