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

	"gosend/internal/buildinfo"
	"gosend/internal/config"
	"gosend/internal/identity"
	"gosend/internal/localsend"
	"gosend/internal/store"
	gosendweb "gosend/web"
)

const shutdownTimeout = 10 * time.Second

type App struct {
	config config.Config
	logger *slog.Logger
	server *http.Server
	store  store.Store
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, directory := range []string{cfg.DataDirectory, cfg.SendDirectory, cfg.ReceiveDirectory} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return nil, fmt.Errorf("create directory %q: %w", directory, err)
		}
	}

	database, err := store.Open(ctx, store.Config{
		Driver: cfg.DatabaseDriver,
		DSN:    cfg.DatabaseDSN,
	})
	if err != nil {
		return nil, err
	}
	localIdentity, err := identity.LoadOrCreate(cfg.DataDirectory)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	handler, err := newHandler(cfg, database, localIdentity)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	logger.Info(
		"runtime initialized",
		"database_driver", cfg.DatabaseDriver,
		"fingerprint", localIdentity.Fingerprint,
		"build_version", buildinfo.Version,
	)

	return &App{
		config: cfg,
		logger: logger,
		store:  database,
		server: &http.Server{
			Addr:              cfg.WebAddress,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	defer func() {
		if err := a.store.Close(); err != nil {
			a.logger.Error("close database", "error", err)
		}
	}()
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

func newHandler(cfg config.Config, database store.Store, localIdentity identity.Identity) (http.Handler, error) {
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
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		ready := databaseReady(request.Context(), database)
		response.Header().Set("Content-Type", "application/json")
		if !ready {
			response.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"ready": ready})
	})
	mux.HandleFunc("GET /api/v1/status", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"alias":                cfg.Alias,
			"protocol":             "LocalSend",
			"protocolVersion":      localsend.ProtocolVersion,
			"specificationVersion": localsend.SpecificationVersion,
			"fingerprint":          localIdentity.Fingerprint,
			"database":             cfg.DatabaseDriver,
			"build":                buildinfo.Current(),
			"ready":                databaseReady(request.Context(), database),
		})
	})
	return mux, nil
}

func databaseReady(ctx context.Context, database store.Store) bool {
	pingContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return database.Ping(pingContext) == nil
}
