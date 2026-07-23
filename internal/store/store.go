package store

import (
	"context"
	"errors"
	"time"

	"gosend/internal/domain"
)

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

type Store interface {
	Ping(context.Context) error
	Close() error

	GetSetting(context.Context, string) (string, error)
	SetSetting(context.Context, string, string) error

	UpsertTrustedDevice(context.Context, domain.TrustedDevice) error
	ListTrustedDevices(context.Context) ([]domain.TrustedDevice, error)
	DeleteTrustedDevice(context.Context, string) error

	CreateTransfer(context.Context, domain.TransferSession, []domain.TransferFile) error
	GetTransfer(context.Context, string) (domain.Transfer, error)
	ListTransfers(context.Context, int) ([]domain.TransferSession, error)
	UpdateTransferStatus(context.Context, string, domain.TransferStatus, string, *time.Time) error
	UpdateTransferFile(context.Context, string, domain.FileStatus, int64, string) error
}
