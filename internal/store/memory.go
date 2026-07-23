package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"gosend/internal/domain"
	"gosend/internal/localsend"
)

type Memory struct {
	mu        sync.RWMutex
	closed    bool
	settings  map[string]string
	devices   map[string]domain.TrustedDevice
	transfers map[string]domain.Transfer
}

func NewMemory() *Memory {
	return &Memory{
		settings:  make(map[string]string),
		devices:   make(map[string]domain.TrustedDevice),
		transfers: make(map[string]domain.Transfer),
	}
}

func (store *Memory) Ping(context.Context) error {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return ErrNotFound
	}
	return nil
}

func (store *Memory) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.closed = true
	return nil
}

func (store *Memory) GetSetting(_ context.Context, key string) (string, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.settings[key]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (store *Memory) SetSetting(_ context.Context, key, value string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.settings[key] = value
	return nil
}

func (store *Memory) UpsertTrustedDevice(_ context.Context, device domain.TrustedDevice) error {
	device.Fingerprint = localsend.NormalizeFingerprint(device.Fingerprint)
	if err := device.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()

	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.devices[device.Fingerprint]; ok {
		device.CreatedAt = existing.CreatedAt
	}
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	if device.UpdatedAt.IsZero() {
		device.UpdatedAt = now
	}
	store.devices[device.Fingerprint] = device
	return nil
}

func (store *Memory) ListTrustedDevices(context.Context) ([]domain.TrustedDevice, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	devices := make([]domain.TrustedDevice, 0, len(store.devices))
	for _, device := range store.devices {
		devices = append(devices, device)
	}
	return deduplicateTrustedDevices(devices), nil
}

func (store *Memory) DeleteTrustedDevice(_ context.Context, fingerprint string) error {
	fingerprint = localsend.NormalizeFingerprint(fingerprint)
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.devices[fingerprint]; !ok {
		return ErrNotFound
	}
	delete(store.devices, fingerprint)
	return nil
}

func (store *Memory) CreateTransfer(_ context.Context, session domain.TransferSession, files []domain.TransferFile) error {
	if err := session.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = session.CreatedAt
	}

	normalizedFiles := make([]domain.TransferFile, len(files))
	for index, file := range files {
		if file.SessionID == "" {
			file.SessionID = session.ID
		}
		if file.SessionID != session.ID {
			return ErrConflict
		}
		if err := file.Validate(); err != nil {
			return err
		}
		if file.CreatedAt.IsZero() {
			file.CreatedAt = now
		}
		if file.UpdatedAt.IsZero() {
			file.UpdatedAt = file.CreatedAt
		}
		normalizedFiles[index] = file
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.transfers[session.ID]; ok {
		return ErrConflict
	}
	knownFileIDs := make(map[string]struct{})
	for _, transfer := range store.transfers {
		for _, file := range transfer.Files {
			knownFileIDs[file.ID] = struct{}{}
		}
	}
	for _, file := range normalizedFiles {
		if _, exists := knownFileIDs[file.ID]; exists {
			return ErrConflict
		}
		knownFileIDs[file.ID] = struct{}{}
	}
	store.transfers[session.ID] = domain.Transfer{Session: session, Files: normalizedFiles}
	return nil
}

func (store *Memory) GetTransfer(_ context.Context, id string) (domain.Transfer, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	transfer, ok := store.transfers[id]
	if !ok {
		return domain.Transfer{}, ErrNotFound
	}
	transfer.Files = append([]domain.TransferFile(nil), transfer.Files...)
	return transfer, nil
}

func (store *Memory) ListTransfers(_ context.Context, limit int) ([]domain.TransferSession, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	sessions := make([]domain.TransferSession, 0, len(store.transfers))
	for _, transfer := range store.transfers {
		sessions = append(sessions, transfer.Session)
	}
	sort.Slice(sessions, func(left, right int) bool {
		return sessions[left].CreatedAt.After(sessions[right].CreatedAt)
	})
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func (store *Memory) DeleteTransfer(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.transfers[id]; !ok {
		return ErrNotFound
	}
	delete(store.transfers, id)
	return nil
}

func (store *Memory) DeleteTransferFile(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for sessionID, transfer := range store.transfers {
		for index := range transfer.Files {
			if transfer.Files[index].ID != id {
				continue
			}
			transfer.Files = append(transfer.Files[:index], transfer.Files[index+1:]...)
			if len(transfer.Files) == 0 {
				delete(store.transfers, sessionID)
			} else {
				store.transfers[sessionID] = transfer
			}
			return nil
		}
	}
	return ErrNotFound
}

func (store *Memory) ClearTransfers(context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	clear(store.transfers)
	return nil
}

func (store *Memory) UpdateTransferStatus(
	_ context.Context,
	id string,
	status domain.TransferStatus,
	errorMessage string,
	completedAt *time.Time,
) error {
	if !status.Valid() {
		return ErrConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	transfer, ok := store.transfers[id]
	if !ok {
		return ErrNotFound
	}
	transfer.Session.Status = status
	transfer.Session.Error = errorMessage
	transfer.Session.CompletedAt = completedAt
	transfer.Session.UpdatedAt = time.Now().UTC()
	store.transfers[id] = transfer
	return nil
}

func (store *Memory) UpdateTransferFile(
	_ context.Context,
	id string,
	status domain.FileStatus,
	bytesTransferred int64,
	errorMessage string,
) error {
	if !status.Valid() || bytesTransferred < 0 {
		return ErrConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for sessionID, transfer := range store.transfers {
		for index := range transfer.Files {
			if transfer.Files[index].ID != id {
				continue
			}
			if bytesTransferred > transfer.Files[index].Size {
				return ErrConflict
			}
			transfer.Files[index].Status = status
			transfer.Files[index].BytesTransferred = bytesTransferred
			transfer.Files[index].Error = errorMessage
			transfer.Files[index].UpdatedAt = time.Now().UTC()
			store.transfers[sessionID] = transfer
			return nil
		}
	}
	return ErrNotFound
}
