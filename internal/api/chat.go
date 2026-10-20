package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

		// streaming: one SSE response to the client, one Ollama stream per tool round behind it
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		// once anything is written the 200 is sent, so later errors can only go inside the stream
		fail := func(msg string) {
			writeEvent(w, flusher, map[string]any{"error": map[string]string{"message": msg}})
		}

		messages := in.Messages
		for round := 0; ; round++ {
			body, err := openStream(r.Context(), ollamaURL, in.Model, in.Think, messages)
			if err != nil {
				if round == 0 { // nothing sent yet: still a real HTTP error (e.g. unknown model)
					http.Error(w, err.Error(), http.StatusBadGateway)
				} else {
					fail(err.Error())
				}
				return
			}
			reply, reason, err := streamRound(w, flusher, body)
			body.Close()
			if err != nil {
				fail(err.Error())
				return
			}

			// no tool calls: this round's text was the final answer
			if len(reply.ToolCalls) == 0 {
				metrics.ObserveRounds(round)
				writeEvent(w, flusher, streamChunk{
					Model:   in.Model,
					Choices: []streamChoice{{Delta: message{Role: "assistant"}, FinishReason: &reason}},
				})
				w.Write([]byte("data: [DONE]\n\n"))
				flusher.Flush()
				return
			}

			if round == maxToolRounds {
				metrics.ObserveRounds(round)
				fail("model kept calling tools; gave up after max rounds")
				return
			}

			// same as the non-streaming loop: the model's order once, then one result per tool
			messages = append(messages, reply)
			for _, order := range reply.ToolCalls {
				result, runErr := tools.Run(order.Function.Name, order.Function.Arguments)
				if runErr != nil {
					result = "error: " + runErr.Error()
				}
				messages = append(messages, message{Role: "tool", ToolName: order.Function.Name, Content: result})
			}
		}
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

// streamRound forwards one Ollama stream to the client as SSE text chunks and returns what the
// model did this round: its full message (all streamed text + any tool calls) and why it stopped.
// It never sends [DONE]: only the caller knows whether another round follows.
// The first write is the first token, so the TTFB metric measures time to first token.
func streamRound(w http.ResponseWriter, flusher http.Flusher, body io.Reader) (message, string, error) {
	var text string       // everything streamed this round; the next round needs it in the history
	var calls []toolsCall // tool calls can come on any line (their own line or the done line)

	scanner := bufio.NewScanner(body)
	for scanner.Scan() { // waits for each new line from Ollama
		var chunk ollamaChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			continue // skip a broken line
		}

		// 1. Ollama failed: the caller reports it
		if chunk.Error != "" {
			return message{}, "", errors.New(chunk.Error)
		}

		calls = append(calls, chunk.Message.ToolCalls...)

		// 2. Last line: token stats arrive here; the round is over
		if chunk.Done {
			metrics.ObserveLLM(chunk.Model, chunk.LLMStats)
			reply := message{Role: "assistant", Content: text, ToolCalls: calls}
			return reply, chunk.DoneReason, nil
		}

		// 3. Normal line: forward the new text right away, and remember it
		if chunk.Message.Content == "" {
			continue
		}
		text += chunk.Message.Content
		err := writeEvent(w, flusher, streamChunk{
			Model:   chunk.Model,
			Choices: []streamChoice{{Delta: message{Role: "assistant", Content: chunk.Message.Content}}},
		})
		if err != nil {
			return message{}, "", err // client disconnected; r.Context() also cancels the Ollama call
		}
	}

	// the loop ended without a done line: Ollama crashed or the connection dropped
	if err := scanner.Err(); err != nil {
		return message{}, "", fmt.Errorf("stream interrupted: %w", err)
	}
	return message{}, "", errors.New("stream ended without a done line")
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

// openStream starts a streaming chat request (with the tool menu) and returns Ollama's open
// response body, which the caller reads with streamRound and then closes.
func openStream(ctx context.Context, ollamaURL, model string, think bool, messages []message) (io.ReadCloser, error) {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   messages,
		"stream":     true,
		"think":      think,
		"keep_alive": "30m",
		"options":    map[string]any{"num_ctx": 8192},
		"tools":      tools.Definitions(), // the menu
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama unreachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, msg)
	}
	return resp.Body, nil // no Close here: the stream is still being written
}
