package routes

import (
	"inference-gateway/internal/api"
	"inference-gateway/internal/config"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Routes(cfg config.Config) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", api.Health)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("POST /completion", api.Completion(cfg.OllamaURL))
	mux.HandleFunc("POST /v1/chat/completions", api.Chat(cfg.OllamaURL))

	// "GET /" matches any GET no other pattern claims, so the API routes above still win.
	if cfg.StaticDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(cfg.StaticDir)))
	}

	return mux
}
