package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gosend/internal/device"
	"gosend/internal/domain"
	"gosend/internal/localsend"
	"gosend/internal/store"
)

func TestSenderTransfersMultipleFilesOverPinnedTLS(t *testing.T) {
	sendDirectory := t.TempDir()
	receiveDirectory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sendDirectory, "documents"), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	files := map[string]string{"documents/one.txt": "one", "two.txt": "second"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sendDirectory, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}

	receiverStore := store.NewMemory()
	receiver, err := NewReceiver(ReceiverConfig{Directory: receiveDirectory, Policy: "auto"}, receiverStore)
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	server := httptest.NewTLSServer(receiverHandler(receiver))
	defer server.Close()
	parsedURL, _ := url.Parse(server.URL)
	host, portText, _ := net.SplitHostPort(parsedURL.Host)
	port, _ := strconv.Atoi(portText)
	sum := sha256.Sum256(server.Certificate().Raw)
	fingerprint := hex.EncodeToString(sum[:])

	registry := device.NewRegistry(0)
	registry.Upsert(localsend.DeviceInfo{
		Alias:       "Receiver",
		Version:     "2.0",
		Fingerprint: fingerprint,
		Port:        port,
		Protocol:    "https",
	}, host)
	senderStore := store.NewMemory()
	sender := NewSender(sendDirectory, senderStore, registry, localsend.DeviceInfo{
		Alias:       "Sender",
		Version:     "2.0",
		Fingerprint: "sender-fingerprint",
		Port:        53317,
		Protocol:    "https",
	})
	progressUpdates := make(chan SendProgress, 1)
	sender.SetOnChange(func() {
		active := sender.Active()
		if len(active) == 0 {
			return
		}
		select {
		case progressUpdates <- active[0]:
		default:
		}
	})
	sessionID, err := sender.Start(context.Background(), fingerprint, []string{"documents/one.txt", "two.txt"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	progress := <-progressUpdates
	if progress.Target.Alias != "Receiver" || progress.Target.Fingerprint != fingerprint {
		t.Fatalf("send progress target = %+v", progress.Target)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		transfer, getErr := senderStore.GetTransfer(context.Background(), sessionID)
		if getErr == nil && transfer.Session.Status == domain.TransferCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	transfer, err := senderStore.GetTransfer(context.Background(), sessionID)
	if err != nil || transfer.Session.Status != domain.TransferCompleted {
		t.Fatalf("outgoing transfer = %+v, %v", transfer, err)
	}
	for name, want := range files {
		content, err := os.ReadFile(filepath.Join(receiveDirectory, name))
		if err != nil || string(content) != want {
			t.Fatalf("received %s = %q, %v", name, content, err)
		}
	}
}

func TestSenderRejectsSourceOutsideDirectory(t *testing.T) {
	directory := t.TempDir()
	sender := NewSender(directory, store.NewMemory(), device.NewRegistry(0), localsend.DeviceInfo{})
	if _, _, err := sender.prepareSources([]string{"../outside.txt"}); err == nil {
		t.Fatal("prepareSources() error = nil, want path boundary error")
	}
}
