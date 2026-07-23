package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gosend/internal/domain"
	"gosend/internal/localsend"
	"gosend/internal/store"
)

func TestReceiverAcceptsMultipleFiles(t *testing.T) {
	directory := t.TempDir()
	database := store.NewMemory()
	receiver, err := NewReceiver(ReceiverConfig{Directory: directory, Policy: "auto"}, database)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	handler := receiverHandler(receiver)
	contents := map[string][]byte{
		"one": []byte("one"),
		"two": []byte("second"),
	}
	files := make(map[string]localsend.FileInfo)
	for id, content := range contents {
		sum := sha256.Sum256(content)
		files[id] = localsend.FileInfo{
			ID:       id,
			FileName: id + ".txt",
			Size:     int64(len(content)),
			FileType: "text/plain",
			SHA256:   hex.EncodeToString(sum[:]),
		}
	}
	prepared := prepareUpload(t, handler, files)

	for id, content := range contents {
		url := "/api/localsend/v2/upload?sessionId=" + prepared.SessionID +
			"&fileId=" + id + "&token=" + prepared.Files[id]
		request := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(content))
		request.RemoteAddr = "192.168.1.20:50000"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("upload %s status = %d, body = %s", id, response.Code, response.Body.String())
		}
		stored, err := os.ReadFile(filepath.Join(directory, id+".txt"))
		if err != nil || !bytes.Equal(stored, content) {
			t.Fatalf("stored %s = %q, %v", id, stored, err)
		}
	}

	transfer, err := database.GetTransfer(context.Background(), prepared.SessionID)
	if err != nil {
		t.Fatalf("GetTransfer() error = %v", err)
	}
	if transfer.Session.Status != domain.TransferCompleted || len(transfer.Files) != 2 {
		t.Fatalf("transfer = %+v", transfer)
	}
	for _, file := range transfer.Files {
		if file.Status != domain.FileCompleted || file.BytesTransferred != file.Size {
			t.Fatalf("file = %+v", file)
		}
	}
}

func TestReceiverRejectsUnsafeFileName(t *testing.T) {
	receiver, err := NewReceiver(ReceiverConfig{Directory: t.TempDir(), Policy: "auto"}, store.NewMemory())
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	handler := receiverHandler(receiver)
	requestBody := localsend.PrepareUploadRequest{
		Info: localsend.DeviceInfo{Alias: "Phone", Fingerprint: "peer"},
		Files: map[string]localsend.FileInfo{
			"bad": {ID: "bad", FileName: "../outside.txt", Size: 1},
		},
	}
	body, _ := json.Marshal(requestBody)
	request := httptest.NewRequest(http.MethodPost, "/api/localsend/v2/prepare-upload", bytes.NewReader(body))
	request.RemoteAddr = "192.168.1.20:50000"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestReceiverManualDecision(t *testing.T) {
	receiver, err := NewReceiver(ReceiverConfig{Directory: t.TempDir(), Policy: "manual"}, store.NewMemory())
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	handler := receiverHandler(receiver)
	body, _ := json.Marshal(localsend.PrepareUploadRequest{
		Info: localsend.DeviceInfo{Alias: "Phone", Fingerprint: "peer"},
		Files: map[string]localsend.FileInfo{
			"one": {ID: "one", FileName: "one.txt", Size: 1},
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/localsend/v2/prepare-upload", bytes.NewReader(body))
	request.RemoteAddr = "192.168.1.20:50000"
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()

	var pending []PendingRequest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending = receiver.Pending()
		if len(pending) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatal("manual request did not become pending")
	}
	if err := receiver.Decide(pending[0].ID, true); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prepare request did not complete after decision")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestReceiverBindsTokenAndCancelToSession(t *testing.T) {
	database := store.NewMemory()
	receiver, err := NewReceiver(ReceiverConfig{Directory: t.TempDir(), Policy: "auto"}, database)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	handler := receiverHandler(receiver)
	files := map[string]localsend.FileInfo{
		"one": {ID: "one", FileName: "one.txt", Size: 3},
	}
	prepared := prepareUpload(t, handler, files)

	wrongToken := httptest.NewRequest(
		http.MethodPost,
		"/api/localsend/v2/upload?sessionId="+prepared.SessionID+"&fileId=one&token=wrong",
		bytes.NewBufferString("one"),
	)
	wrongToken.RemoteAddr = "192.168.1.20:50000"
	wrongResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongResponse, wrongToken)
	if wrongResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong token status = %d, want 403", wrongResponse.Code)
	}

	cancel := httptest.NewRequest(
		http.MethodPost,
		"/api/localsend/v2/cancel?sessionId="+prepared.SessionID,
		nil,
	)
	cancel.RemoteAddr = "192.168.1.20:50000"
	cancelResponse := httptest.NewRecorder()
	handler.ServeHTTP(cancelResponse, cancel)
	if cancelResponse.Code != http.StatusOK {
		t.Fatalf("cancel status = %d", cancelResponse.Code)
	}

	afterCancel := httptest.NewRequest(
		http.MethodPost,
		"/api/localsend/v2/upload?sessionId="+prepared.SessionID+"&fileId=one&token="+prepared.Files["one"],
		bytes.NewBufferString("one"),
	)
	afterCancel.RemoteAddr = "192.168.1.20:50000"
	afterResponse := httptest.NewRecorder()
	handler.ServeHTTP(afterResponse, afterCancel)
	if afterResponse.Code != http.StatusForbidden {
		t.Fatalf("upload after cancel status = %d, want 403", afterResponse.Code)
	}
	transfer, err := database.GetTransfer(context.Background(), prepared.SessionID)
	if err != nil || transfer.Session.Status != domain.TransferCancelled {
		t.Fatalf("cancelled transfer = %+v, %v", transfer, err)
	}
}

func TestReceiverRejectsHashMismatch(t *testing.T) {
	directory := t.TempDir()
	database := store.NewMemory()
	receiver, err := NewReceiver(ReceiverConfig{Directory: directory, Policy: "auto"}, database)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	handler := receiverHandler(receiver)
	files := map[string]localsend.FileInfo{
		"one": {
			ID:       "one",
			FileName: "one.txt",
			Size:     3,
			SHA256:   strings.Repeat("0", 64),
		},
	}
	prepared := prepareUpload(t, handler, files)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/localsend/v2/upload?sessionId="+prepared.SessionID+"&fileId=one&token="+prepared.Files["one"],
		bytes.NewBufferString("one"),
	)
	request.RemoteAddr = "192.168.1.20:50000"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if _, err := os.Stat(filepath.Join(directory, "one.txt")); !os.IsNotExist(err) {
		t.Fatalf("target exists after hash mismatch: %v", err)
	}
}

func prepareUpload(
	t *testing.T,
	handler http.Handler,
	files map[string]localsend.FileInfo,
) localsend.PrepareUploadResponse {
	t.Helper()
	body, _ := json.Marshal(localsend.PrepareUploadRequest{
		Info:  localsend.DeviceInfo{Alias: "Phone", Fingerprint: "peer"},
		Files: files,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/localsend/v2/prepare-upload", bytes.NewReader(body))
	request.RemoteAddr = "192.168.1.20:50000"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", response.Code, response.Body.String())
	}
	var prepared localsend.PrepareUploadResponse
	if err := json.NewDecoder(response.Body).Decode(&prepared); err != nil {
		t.Fatalf("decode prepare response: %v", err)
	}
	return prepared
}

func receiverHandler(receiver *Receiver) http.Handler {
	mux := http.NewServeMux()
	receiver.RegisterRoutes(mux)
	return mux
}
