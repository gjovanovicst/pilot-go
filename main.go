// Command pilot-go runs the pilot-go HTTP API.
//
// Every request that reaches the API emits one structured JSON log line on
// stdout carrying the request method, path, response status and handler
// duration in milliseconds.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pilot-go/middleware"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           newHandler(logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Stop accepting new work on SIGINT/SIGTERM and let in-flight requests
	// finish, so each of them still gets its log line.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
	}
}

// newHandler builds the API routes and wraps them with the request logging
// middleware. Logging is the outermost layer so every request, on every route,
// emits exactly one log line.
func newHandler(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Sample route so the server has something to serve while the real API is
	// being built out.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"pilot-go"}`))
	})

	return middleware.Logging(logger)(mux)
}
