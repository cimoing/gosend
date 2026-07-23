package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gosend/internal/domain"
	"gosend/internal/store"
)

type storeFactory struct {
	name string
	open func(*testing.T) store.Store
}

func TestStoreContract(t *testing.T) {
	factories := []storeFactory{
		{name: "memory", open: func(*testing.T) store.Store { return store.NewMemory() }},
		{
			name: "sqlite",
			open: func(t *testing.T) store.Store {
				instance, err := store.Open(context.Background(), store.Config{
					Driver: "sqlite",
					DSN:    filepath.Join(t.TempDir(), "gosend.db"),
				})
				if err != nil {
					t.Fatalf("store.Open() error = %v", err)
				}
				return instance
			},
		},
	}
	if dsn := os.Getenv("GOSEND_TEST_MYSQL_DSN"); dsn != "" {
		factories = append(factories, storeFactory{
			name: "mysql",
			open: func(t *testing.T) store.Store {
				instance, err := store.Open(context.Background(), store.Config{Driver: "mysql", DSN: dsn})
				if err != nil {
					t.Fatalf("store.Open() error = %v", err)
				}
				return instance
			},
		})
	}
	if dsn := os.Getenv("GOSEND_TEST_POSTGRES_DSN"); dsn != "" {
		factories = append(factories, storeFactory{
			name: "postgres",
			open: func(t *testing.T) store.Store {
				instance, err := store.Open(context.Background(), store.Config{Driver: "postgres", DSN: dsn})
				if err != nil {
					t.Fatalf("store.Open() error = %v", err)
				}
				return instance
			},
		})
	}

	for _, factory := range factories {
		t.Run(factory.name, func(t *testing.T) {
			runStoreContract(t, factory.open(t))
		})
	}
}

func runStoreContract(t *testing.T, database store.Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	settingKey := "receive_policy_" + suffix

	if err := database.SetSetting(ctx, settingKey, "trusted"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}
	value, err := database.GetSetting(ctx, settingKey)
	if err != nil || value != "trusted" {
		t.Fatalf("GetSetting() = %q, %v; want trusted, nil", value, err)
	}
	if _, err := database.GetSetting(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSetting(missing) error = %v, want ErrNotFound", err)
	}

	device := domain.TrustedDevice{Fingerprint: strings.ToUpper("fingerprint-" + suffix), Alias: "Kitchen NAS"}
	if err := database.UpsertTrustedDevice(ctx, device); err != nil {
		t.Fatalf("UpsertTrustedDevice() error = %v", err)
	}
	device.Alias = "Main NAS"
	if err := database.UpsertTrustedDevice(ctx, device); err != nil {
		t.Fatalf("UpsertTrustedDevice(update) error = %v", err)
	}
	devices, err := database.ListTrustedDevices(ctx)
	if err != nil || len(devices) != 1 || devices[0].Alias != "Main NAS" ||
		devices[0].Fingerprint != strings.ToLower(device.Fingerprint) {
		t.Fatalf("ListTrustedDevices() = %+v, %v", devices, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	session := domain.TransferSession{
		ID:              "session-" + suffix,
		Direction:       domain.TransferIncoming,
		PeerFingerprint: device.Fingerprint,
		PeerAlias:       device.Alias,
		Status:          domain.TransferPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	files := []domain.TransferFile{
		{
			ID:        "file-1-" + suffix,
			SessionID: session.ID,
			FileName:  "one.txt",
			Size:      3,
			MIMEType:  "text/plain",
			Status:    domain.FilePending,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "file-2-" + suffix,
			SessionID: session.ID,
			FileName:  "two.txt",
			Size:      6,
			MIMEType:  "text/plain",
			Status:    domain.FilePending,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := database.CreateTransfer(ctx, session, files); err != nil {
		t.Fatalf("CreateTransfer() error = %v", err)
	}
	if err := database.CreateTransfer(ctx, session, files); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate CreateTransfer() error = %v, want ErrConflict", err)
	}
	if err := database.UpdateTransferFile(ctx, "file-1-"+suffix, domain.FileActive, 4, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("oversized UpdateTransferFile() error = %v, want ErrConflict", err)
	}
	if err := database.UpdateTransferFile(ctx, "file-1-"+suffix, domain.FileCompleted, 3, ""); err != nil {
		t.Fatalf("UpdateTransferFile() error = %v", err)
	}
	completedAt := now.Add(time.Second)
	if err := database.UpdateTransferStatus(ctx, session.ID, domain.TransferCompleted, "", &completedAt); err != nil {
		t.Fatalf("UpdateTransferStatus() error = %v", err)
	}

	transfer, err := database.GetTransfer(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetTransfer() error = %v", err)
	}
	if transfer.Session.Status != domain.TransferCompleted || transfer.Session.CompletedAt == nil {
		t.Fatalf("GetTransfer() session = %+v", transfer.Session)
	}
	if len(transfer.Files) != 2 || transfer.Files[0].Status != domain.FileCompleted {
		t.Fatalf("GetTransfer() files = %+v", transfer.Files)
	}
	sessions, err := database.ListTransfers(ctx, 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListTransfers() = %+v, %v", sessions, err)
	}
	if err := database.DeleteTransferFile(ctx, "file-1-"+suffix); err != nil {
		t.Fatalf("DeleteTransferFile() error = %v", err)
	}
	transfer, err = database.GetTransfer(ctx, session.ID)
	if err != nil || len(transfer.Files) != 1 || transfer.Files[0].ID != "file-2-"+suffix {
		t.Fatalf("GetTransfer() after file delete = %+v, %v", transfer, err)
	}
	if err := database.DeleteTransferFile(ctx, "missing-file"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteTransferFile(missing) error = %v, want ErrNotFound", err)
	}
	if err := database.DeleteTransfer(ctx, session.ID); err != nil {
		t.Fatalf("DeleteTransfer() error = %v", err)
	}
	if err := database.DeleteTransfer(ctx, session.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteTransfer(missing) error = %v, want ErrNotFound", err)
	}

	session.ID = "clear-session-" + suffix
	files = []domain.TransferFile{{
		ID:        "clear-file-" + suffix,
		SessionID: session.ID,
		FileName:  "clear.txt",
		Size:      1,
		Status:    domain.FilePending,
		CreatedAt: now,
		UpdatedAt: now,
	}}
	if err := database.CreateTransfer(ctx, session, files); err != nil {
		t.Fatalf("CreateTransfer(clear) error = %v", err)
	}
	if err := database.ClearTransfers(ctx); err != nil {
		t.Fatalf("ClearTransfers() error = %v", err)
	}
	sessions, err = database.ListTransfers(ctx, 10)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("ListTransfers() after clear = %+v, %v", sessions, err)
	}

	if err := database.DeleteTrustedDevice(ctx, device.Fingerprint); err != nil {
		t.Fatalf("DeleteTrustedDevice() error = %v", err)
	}
	if err := database.DeleteTrustedDevice(ctx, device.Fingerprint); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteTrustedDevice(missing) error = %v, want ErrNotFound", err)
	}
}
