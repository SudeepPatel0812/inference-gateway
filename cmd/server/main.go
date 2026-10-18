package main

import (
	"context"
	"errors"
	"inference-gateway/internal/config"
	"inference-gateway/internal/metrics"
	"inference-gateway/internal/routes"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("Starting server failed at port", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: metrics.Middleware(routes.Routes(cfg)),
	}

	srvErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		srvErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-srvErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
