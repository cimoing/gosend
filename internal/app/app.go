package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gosend/internal/buildinfo"
	"gosend/internal/config"
	"gosend/internal/device"
	"gosend/internal/discovery"
	"gosend/internal/domain"
	"gosend/internal/identity"
	"gosend/internal/localsend"
	"gosend/internal/store"
	"gosend/internal/transfer"
	gosendweb "gosend/web"
)

const shutdownTimeout = 10 * time.Second

type App struct {
	config    config.Config
	logger    *slog.Logger
	server    *http.Server
	store     store.Store
	nearby    *device.Registry
	discovery *discovery.Service
	receiver  *transfer.Receiver
	sender    *transfer.Sender
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
	receiver, err := transfer.NewReceiver(transfer.ReceiverConfig{
		Directory: cfg.ReceiveDirectory,
		Policy:    cfg.ReceivePolicy,
	}, database)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	nearby := device.NewRegistry(0)
	discoveryService := discovery.New(discovery.Config{
		Alias:          cfg.Alias,
		Port:           cfg.LocalSendPort,
		Fingerprint:    localIdentity.Fingerprint,
		Certificate:    localIdentity.Certificate,
		RegisterRoutes: receiver.RegisterRoutes,
	}, nearby, logger)
	sender := transfer.NewSender(cfg.SendDirectory, database, nearby, discoveryService.SelfInfo(false))
	handler, err := newHandler(
		cfg,
		database,
		localIdentity,
		nearby,
		receiver,
		sender,
		discoveryService.TriggerScan,
	)
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
		config:    cfg,
		logger:    logger,
		store:     database,
		nearby:    nearby,
		discovery: discoveryService,
		receiver:  receiver,
		sender:    sender,
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
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		component string
		err       error
	}
	results := make(chan result, 2)
	go func() {
		err := a.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			results <- result{component: "Web server", err: err}
			return
		}
		results <- result{component: "Web server"}
	}()
	go func() {
		err := a.discovery.Run(runContext)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		results <- result{component: "LocalSend discovery", err: err}
	}()

	var runErr error
	completed := 0
	select {
	case found := <-results:
		completed++
		if found.err != nil {
			runErr = fmt.Errorf("%s stopped: %w", found.component, found.err)
		} else if ctx.Err() == nil {
			runErr = fmt.Errorf("%s stopped unexpectedly", found.component)
		}
	case <-ctx.Done():
		runErr = ctx.Err()
	}
	cancel()
	a.sender.CancelAll()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := a.server.Shutdown(shutdownContext); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down Web server: %w", err)
	}
	for completed < 2 {
		select {
		case found := <-results:
			completed++
			if found.err != nil && runErr == nil {
				runErr = fmt.Errorf("%s stopped: %w", found.component, found.err)
			}
		case <-shutdownContext.Done():
			if runErr == nil {
				runErr = errors.New("timed out waiting for services to stop")
			}
			completed = 2
		}
	}
	return runErr
}

func newHandler(
	cfg config.Config,
	database store.Store,
	localIdentity identity.Identity,
	nearby *device.Registry,
	receiver *transfer.Receiver,
	sender *transfer.Sender,
	triggerDiscovery func() bool,
) (http.Handler, error) {
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
			"receivePolicy":        cfg.ReceivePolicy,
			"build":                buildinfo.Current(),
			"ready":                databaseReady(request.Context(), database),
			"nearbyDevices":        len(nearby.List()),
		})
	})
	mux.HandleFunc("GET /api/v1/devices", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"devices": nearby.List()})
	})
	mux.HandleFunc("POST /api/v1/discovery/scan", func(response http.ResponseWriter, _ *http.Request) {
		if triggerDiscovery == nil {
			http.Error(response, "discovery scan unavailable", http.StatusServiceUnavailable)
			return
		}
		if !triggerDiscovery() {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(response).Encode(map[string]any{"started": false, "running": true})
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(response).Encode(map[string]any{"started": true, "running": true})
	})
	mux.HandleFunc("GET /api/v1/receive-requests", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"requests": receiver.Pending()})
	})
	mux.HandleFunc("POST /api/v1/receive-requests/{id}/{decision}", func(response http.ResponseWriter, request *http.Request) {
		var accept bool
		switch request.PathValue("decision") {
		case "accept":
			accept = true
		case "reject":
			accept = false
		default:
			http.Error(response, "invalid decision", http.StatusBadRequest)
			return
		}
		err := receiver.Decide(request.PathValue("id"), accept)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(response, "request not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(response, "request already decided", http.StatusConflict)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/v1/send", func(response http.ResponseWriter, request *http.Request) {
		var input struct {
			Fingerprint string   `json:"fingerprint"`
			Files       []string `json:"files"`
			PIN         string   `json:"pin"`
		}
		request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
		defer request.Body.Close()
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			http.Error(response, "invalid body", http.StatusBadRequest)
			return
		}
		sessionID, err := sender.Start(context.Background(), input.Fingerprint, input.Files, input.PIN)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(response).Encode(map[string]string{"sessionId": sessionID})
	})
	mux.HandleFunc("GET /api/v1/send-progress", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"sessions": sender.Active()})
	})
	mux.HandleFunc("GET /api/v1/files", func(response http.ResponseWriter, _ *http.Request) {
		files, err := listSendFiles(cfg.SendDirectory)
		if err != nil {
			http.Error(response, "list files failed", http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"files": files})
	})
	mux.HandleFunc("GET /api/v1/trusted-devices", func(response http.ResponseWriter, request *http.Request) {
		devices, err := database.ListTrustedDevices(request.Context())
		if err != nil {
			http.Error(response, "list trusted devices failed", http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"devices": devices})
	})
	mux.HandleFunc("POST /api/v1/trusted-devices/{fingerprint}", func(response http.ResponseWriter, request *http.Request) {
		found, ok := nearby.Get(request.PathValue("fingerprint"))
		if !ok {
			http.Error(response, "online device not found", http.StatusNotFound)
			return
		}
		now := time.Now().UTC()
		err := database.UpsertTrustedDevice(request.Context(), domain.TrustedDevice{
			Fingerprint: found.Info.Fingerprint,
			Alias:       found.Info.Alias,
			DeviceModel: found.Info.DeviceModel,
			DeviceType:  found.Info.DeviceType,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		if err != nil {
			http.Error(response, "trust device failed", http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v1/trusted-devices/{fingerprint}", func(response http.ResponseWriter, request *http.Request) {
		err := database.DeleteTrustedDevice(request.Context(), request.PathValue("fingerprint"))
		if errors.Is(err, store.ErrNotFound) {
			http.Error(response, "trusted device not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(response, "delete trusted device failed", http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/transfers", func(response http.ResponseWriter, request *http.Request) {
		sessions, err := database.ListTransfers(request.Context(), 200)
		if err != nil {
			http.Error(response, "list transfers failed", http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"transfers": sessions})
	})
	mux.HandleFunc("GET /api/v1/transfers/{id}", func(response http.ResponseWriter, request *http.Request) {
		found, err := database.GetTransfer(request.Context(), request.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			http.Error(response, "transfer not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(response, "get transfer failed", http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(found)
	})
	mux.HandleFunc("GET /api/v1/events", func(response http.ResponseWriter, request *http.Request) {
		flusher, ok := response.(http.Flusher)
		if !ok {
			http.Error(response, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		response.Header().Set("X-Accel-Buffering", "no")
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			snapshot := map[string]any{
				"devices": nearby.List(),
				"pending": receiver.Pending(),
				"sending": sender.Active(),
			}
			payload, _ := json.Marshal(snapshot)
			_, _ = fmt.Fprintf(response, "event: snapshot\ndata: %s\n\n", payload)
			flusher.Flush()
			select {
			case <-request.Context().Done():
				return
			case <-ticker.C:
			}
		}
	})
	mux.HandleFunc("POST /api/v1/send/{id}/cancel", func(response http.ResponseWriter, request *http.Request) {
		if err := sender.Cancel(request.PathValue("id")); errors.Is(err, store.ErrNotFound) {
			http.Error(response, "session not found", http.StatusNotFound)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	return secureHandler(mux, cfg.WebAuthToken), nil
}

func secureHandler(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		if token != "" && request.URL.Path != "/healthz" && request.URL.Path != "/readyz" {
			username, password, ok := request.BasicAuth()
			validUser := subtle.ConstantTimeCompare([]byte(username), []byte("gosend")) == 1
			validPassword := len(password) == len(token) &&
				subtle.ConstantTimeCompare([]byte(password), []byte(token)) == 1
			if !ok || !validUser || !validPassword {
				response.Header().Set("WWW-Authenticate", `Basic realm="GoSend", charset="UTF-8"`)
				http.Error(response, "authentication required", http.StatusUnauthorized)
				return
			}
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions {
			if origin := request.Header.Get("Origin"); origin != "" && !sameOrigin(origin, request.Host) {
				http.Error(response, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(response, request)
	})
}

func sameOrigin(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, host) && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

type sendFile struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func listSendFiles(root string) ([]sendFile, error) {
	var files []sendFile
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("file escaped send directory")
		}
		files = append(files, sendFile{
			Path:     filepath.ToSlash(relative),
			Name:     entry.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
		return nil
	})
	return files, err
}

func databaseReady(ctx context.Context, database store.Store) bool {
	pingContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return database.Ping(pingContext) == nil
}
