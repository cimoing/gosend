package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gosend/internal/config"
	"gosend/internal/device"
	"gosend/internal/identity"
	"gosend/internal/store"
)

func TestNewInitializesSQLiteAndIdentity(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Alias:            "Test GoSend",
		WebAddress:       "127.0.0.1:0",
		LocalSendPort:    53317,
		DataDirectory:    root,
		SendDirectory:    filepath.Join(root, "send"),
		ReceiveDirectory: filepath.Join(root, "receive"),
		DatabaseDriver:   "sqlite",
		DatabaseDSN:      filepath.Join(root, "gosend.db"),
	}
	application, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = application.store.Close() })

	for _, path := range []string{
		cfg.SendDirectory,
		cfg.ReceiveDirectory,
		cfg.DatabaseDSN,
		filepath.Join(root, "identity.pem"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Stat(%q) error = %v", path, err)
		}
	}
}

func TestHandlerServesHealthAndWebUI(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Alias:            "Test GoSend",
		WebAddress:       ":0",
		LocalSendPort:    53317,
		DataDirectory:    root,
		SendDirectory:    filepath.Join(root, "send"),
		ReceiveDirectory: filepath.Join(root, "receive"),
		DatabaseDriver:   "memory",
	}
	database := store.NewMemory()
	t.Cleanup(func() { _ = database.Close() })
	handler, err := newHandler(
		cfg,
		database,
		identity.Identity{Fingerprint: "test-fingerprint"},
		device.NewRegistry(0),
	)
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/healthz", want: `"status":"ok"`},
		{path: "/readyz", want: `"ready":true`},
		{path: "/api/v1/status", want: `"alias":"Test GoSend"`},
		{path: "/api/v1/devices", want: `"devices":[]`},
		{path: "/", want: "GoSend"},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		result := response.Result()
		body, readErr := io.ReadAll(result.Body)
		_ = result.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s response: %v", test.path, readErr)
		}
		if result.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d, want 200", test.path, result.StatusCode)
		}
		if !strings.Contains(string(body), test.want) {
			t.Errorf("%s body = %q, want substring %q", test.path, body, test.want)
		}
	}
}
