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
	"gosend/internal/localsend"
	"gosend/internal/store"
	"gosend/internal/transfer"
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
		ReceivePolicy:    "manual",
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
	receiver, err := transfer.NewReceiver(transfer.ReceiverConfig{
		Directory: cfg.ReceiveDirectory,
		Policy:    "manual",
	}, database)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	nearby := device.NewRegistry(0)
	sender := transfer.NewSender(cfg.SendDirectory, database, nearby, identityToInfo("test-fingerprint"))
	handler, err := newHandler(
		cfg,
		database,
		identity.Identity{Fingerprint: "test-fingerprint"},
		nearby,
		receiver,
		sender,
		func() bool { return true },
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
		{path: "/api/v1/receive-requests", want: `"requests":[]`},
		{path: "/api/v1/send-progress", want: `"sessions":[]`},
		{path: "/", want: "接收文件"},
		{path: "/app.js", want: "const state="},
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

	scanRequest := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/scan", nil)
	scanResponse := httptest.NewRecorder()
	handler.ServeHTTP(scanResponse, scanRequest)
	if scanResponse.Code != http.StatusAccepted || !strings.Contains(scanResponse.Body.String(), `"started":true`) {
		t.Fatalf("discovery scan response = %d %q", scanResponse.Code, scanResponse.Body.String())
	}
}

func TestListSendFiles(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "report.txt"), []byte("report"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	files, err := listSendFiles(root)
	if err != nil {
		t.Fatalf("listSendFiles() error = %v", err)
	}
	if len(files) != 1 || files[0].Path != "nested/report.txt" || files[0].Size != 6 {
		t.Fatalf("listSendFiles() = %+v", files)
	}
}

func TestSecureHandlerAuthenticationAndOrigin(t *testing.T) {
	handler := secureHandler(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), "secret")

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gosend.local/api/v1/send", nil)
	request.SetBasicAuth("gosend", "secret")
	request.Header.Set("Origin", "http://evil.local")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", forbidden.Code)
	}
}

func identityToInfo(fingerprint string) localsend.DeviceInfo {
	return localsend.DeviceInfo{
		Alias:       "Test GoSend",
		Version:     localsend.ProtocolVersion,
		Fingerprint: fingerprint,
		Port:        53317,
		Protocol:    "https",
	}
}
