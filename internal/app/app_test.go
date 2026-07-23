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
	"time"

	"gosend/internal/config"
	"gosend/internal/device"
	"gosend/internal/domain"
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
		{path: "/", want: "传入请求"},
		{path: "/app.js", want: "const state"},
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
		if test.path == "/app.js" && strings.Contains(string(body), "setInterval(") {
			t.Error("app.js contains timer-based polling")
		}
	}

	scanRequest := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/scan", nil)
	scanResponse := httptest.NewRecorder()
	handler.ServeHTTP(scanResponse, scanRequest)
	if scanResponse.Code != http.StatusAccepted || !strings.Contains(scanResponse.Body.String(), `"started":true`) {
		t.Fatalf("discovery scan response = %d %q", scanResponse.Code, scanResponse.Body.String())
	}

	now := time.Now().UTC()
	session := domain.TransferSession{
		ID:        "history-session",
		Direction: domain.TransferIncoming,
		PeerAlias: "Phone",
		Status:    domain.TransferCompleted,
		CreatedAt: now,
		UpdatedAt: now,
	}
	file := domain.TransferFile{
		ID:        "history-file",
		SessionID: session.ID,
		FileName:  "photo.jpg",
		Size:      12,
		Status:    domain.FileCompleted,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := database.CreateTransfer(context.Background(), session, []domain.TransferFile{file}); err != nil {
		t.Fatalf("CreateTransfer(history) error = %v", err)
	}
	historyResponse := httptest.NewRecorder()
	handler.ServeHTTP(historyResponse, httptest.NewRequest(http.MethodGet, "/api/v1/transfers", nil))
	if historyResponse.Code != http.StatusOK ||
		!strings.Contains(historyResponse.Body.String(), `"FileName":"photo.jpg"`) {
		t.Fatalf("history response = %d %q", historyResponse.Code, historyResponse.Body.String())
	}
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		deleteResponse,
		httptest.NewRequest(
			http.MethodDelete,
			"/api/v1/transfers/history-session/files/history-file",
			nil,
		),
	)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete history file response = %d %q", deleteResponse.Code, deleteResponse.Body.String())
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

func TestListSendDirectoryAndExpandSelection(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "documents", "reports")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "root.txt"), []byte("root"), 0o600); err != nil {
		t.Fatalf("WriteFile(root.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "annual.txt"), []byte("annual"), 0o600); err != nil {
		t.Fatalf("WriteFile(annual.txt) error = %v", err)
	}

	directory, err := listSendDirectory(root, "documents")
	if err != nil {
		t.Fatalf("listSendDirectory() error = %v", err)
	}
	if directory.Path != "documents" || directory.Parent != "" || len(directory.Entries) != 1 {
		t.Fatalf("listSendDirectory() = %+v", directory)
	}
	if directory.Entries[0].Type != "directory" || directory.Entries[0].Path != "documents/reports" {
		t.Fatalf("directory entry = %+v", directory.Entries[0])
	}

	selected, err := expandSendSelection(root, []string{"root.txt"}, []string{"documents"})
	if err != nil {
		t.Fatalf("expandSendSelection() error = %v", err)
	}
	if len(selected) != 2 || selected[0] != "documents/reports/annual.txt" || selected[1] != "root.txt" {
		t.Fatalf("expandSendSelection() = %#v", selected)
	}
	if _, err := expandSendSelection(root, nil, []string{"../outside"}); err == nil {
		t.Fatal("expandSendSelection() traversal error = nil")
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
