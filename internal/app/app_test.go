package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"gosend/internal/config"
)

func TestHandlerServesHealthAndWebUI(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Alias:            "Test GoSend",
		WebAddress:       ":0",
		LocalSendPort:    53317,
		DataDirectory:    root,
		SendDirectory:    filepath.Join(root, "send"),
		ReceiveDirectory: filepath.Join(root, "receive"),
	}
	handler, err := newHandler(cfg)
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/healthz", want: `"status":"ok"`},
		{path: "/api/v1/status", want: `"alias":"Test GoSend"`},
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
