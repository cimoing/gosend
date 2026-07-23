package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"gosend/internal/config"
	gosendweb "gosend/web"
)

const shutdownTimeout = 10 * time.Second

type App struct {
	config config.Config
	logger *slog.Logger
	server *http.Server
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, directory := range []string{cfg.DataDirectory, cfg.SendDirectory, cfg.ReceiveDirectory} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return nil, fmt.Errorf("create directory %q: %w", directory, err)
		}
	}

	handler, err := newHandler(cfg)
	if err != nil {
		return nil, err
	}

	return &App{
		config: cfg,
		logger: logger,
		server: &http.Server{
			Addr:              cfg.WebAddress,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() {
		err := a.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := a.server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down Web server: %w", err)
		}
		return ctx.Err()
	}
}

func newHandler(cfg config.Config) (http.Handler, error) {
	staticFiles, err := fs.Sub(gosendweb.Static, "static")
	if err != nil {
		return nil, fmt.Errorf("open embedded Web files: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/status", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"alias":                cfg.Alias,
			"protocol":             "LocalSend",
			"protocolVersion":      "2.0",
			"specificationVersion": "2.1",
			"ready":                false,
		})
	})
	return mux, nil
}
