package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config is the fully-resolved runtime configuration. Everything the service
// needs from its environment lives here and nowhere else, so there is one place
// to look and one place to change.
type Config struct {
	Port      string
	OllamaURL string
}

// Load reads configuration from the process environment, loading a .env file
// first if one is present (convenient for local dev, ignored in real
// environments where the platform sets real env vars).
//
// Missing required values return an error rather than calling log.Fatal: a
// library that kills the process takes the decision away from the caller and
// can't be tested.
func Load() (Config, error) {
	_ = godotenv.Load() // optional; absence is not an error

	cfg := Config{
		Port:      envOr("PORT", "8080"),
		OllamaURL: envOr("OLLAMA_URL", "http://localhost:11434"),
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
