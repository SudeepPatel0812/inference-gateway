// Package metrics exposes Prometheus metrics for HTTP traffic and LLM usage.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// LLM requests take seconds to minutes; the default buckets stop at 10s and would hide the tail.
var (
	buckets      = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120}
	toolBuckets  = []float64{0.001, 0.01, 0.1, 0.5, 1, 2.5, 5, 10}
	roundBuckets = []float64{0, 1, 2, 3, 4, 5}
)

var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"})

	duration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time from request start to handler return.",
		Buckets: buckets,
	}, []string{"route", "method"})

	ttfb = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_time_to_first_byte_seconds",
		Help:    "Time from request start to the first response byte or header.",
		Buckets: buckets,
	}, []string{"route"})

	inFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "Requests currently being served.",
	})

	llmRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_requests_total",
		Help: "Successful model calls by model.",
	}, []string{"model"})

	llmTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_tokens_total",
		Help: "Tokens processed, by model and type (prompt or completion).",
	}, []string{"model", "type"})

	llmPhase = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_phase_seconds_total",
		Help: "Time spent per phase (load, prefill, decode), by model.",
	}, []string{"model", "phase"})
	toolCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_tool_calls_total",
		Help: "Tool executions by tool and outcome (ok or error).",
	}, []string{"tool", "outcome"})
	toolDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_tool_duration_seconds",
		Help:    "Tool execution time by tool (fetch_url can take seconds; calculator takes microseconds).",
		Buckets: toolBuckets,
	}, []string{"tool"})
	toolAgentRounds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "llm_agent_rounds",
		Help:    "Tool rounds a request needed (0 = answered directly).",
		Buckets: roundBuckets,
	})
)

// Middleware records request count, duration, TTFB and in-flight requests for every route.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" { // keep Prometheus' own scrapes out of the traffic numbers
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		inFlight.Inc()
		defer inFlight.Dec()

		rec := &recorder{ResponseWriter: w, start: start, code: http.StatusOK}
		next.ServeHTTP(rec, r)

		// r.Pattern is set by ServeMux to the registered pattern, e.g. "POST /v1/chat/completions".
		// Never label with r.URL.Path: every distinct path would create a new time series.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		if rec.firstByte.IsZero() {
			rec.firstByte = time.Now()
		}

		requests.WithLabelValues(route, r.Method, strconv.Itoa(rec.code)).Inc()
		duration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		ttfb.WithLabelValues(route).Observe(rec.firstByte.Sub(start).Seconds())
	})
}

// recorder captures the status code and the time of the first write.
type recorder struct {
	http.ResponseWriter
	start     time.Time
	firstByte time.Time
	code      int
	wrote     bool
}

func (w *recorder) WriteHeader(code int) {
	if !w.wrote {
		w.wrote, w.code, w.firstByte = true, code, time.Now()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote, w.firstByte = true, time.Now()
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps streaming working (step 2) through the wrapper.
func (w *recorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// LLMStats are the timing and token fields Ollama returns on every non-streamed response.
// Embed it in a response struct to decode them; durations are in nanoseconds.
type LLMStats struct {
	PromptEvalCount    int   `json:"prompt_eval_count"`
	EvalCount          int   `json:"eval_count"`
	LoadDuration       int64 `json:"load_duration"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalDuration       int64 `json:"eval_duration"`
}

// ObserveLLM records token counts and per-phase time for one model call.
func ObserveLLM(model string, s LLMStats) {
	llmRequests.WithLabelValues(model).Inc()
	llmTokens.WithLabelValues(model, "prompt").Add(float64(s.PromptEvalCount))
	llmTokens.WithLabelValues(model, "completion").Add(float64(s.EvalCount))
	llmPhase.WithLabelValues(model, "load").Add(time.Duration(s.LoadDuration).Seconds())
	llmPhase.WithLabelValues(model, "prefill").Add(time.Duration(s.PromptEvalDuration).Seconds())
	llmPhase.WithLabelValues(model, "decode").Add(time.Duration(s.EvalDuration).Seconds())
}

// ObserveTool records one tool execution. tool must be a registered name (or "unknown").
func ObserveTool(tool string, d time.Duration, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	toolCalls.WithLabelValues(tool, outcome).Inc()
	toolDuration.WithLabelValues(tool).Observe(d.Seconds())
}

// ObserveRounds records how many tool rounds one request needed.
func ObserveRounds(n int) {
	toolAgentRounds.Observe(float64(n))
}
