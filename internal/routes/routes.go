package routes

import (
	"inference-gateway/internal/api"
	"inference-gateway/internal/config"
	"net/http"
)

func Routes(cfg config.Config) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", api.Health)
	mux.HandleFunc("POST /completion", api.Completion(cfg.OllamaURL))

	return mux
}
