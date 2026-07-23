package domain

import (
	"errors"
	"time"
)

type TransferDirection string

const (
	TransferIncoming TransferDirection = "incoming"
	TransferOutgoing TransferDirection = "outgoing"
)

type TransferStatus string

const (
	TransferPending   TransferStatus = "pending"
	TransferActive    TransferStatus = "active"
	TransferCompleted TransferStatus = "completed"
	TransferFailed    TransferStatus = "failed"
	TransferCancelled TransferStatus = "cancelled"
	TransferRejected  TransferStatus = "rejected"
)

type FileStatus string

const (
	FilePending   FileStatus = "pending"
	FileActive    FileStatus = "active"
	FileCompleted FileStatus = "completed"
	FileFailed    FileStatus = "failed"
	FileCancelled FileStatus = "cancelled"
	FileRejected  FileStatus = "rejected"
)

type TrustedDevice struct {
	Fingerprint string
	Alias       string
	DeviceModel string
	DeviceType  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TransferSession struct {
	ID              string
	Direction       TransferDirection
	PeerFingerprint string
	PeerAlias       string
	Status          TransferStatus
	Error           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

type TransferFile struct {
	ID               string
	SessionID        string
	FileName         string
	Size             int64
	MIMEType         string
	SHA256           string
	Status           FileStatus
	BytesTransferred int64
	Error            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Transfer struct {
	Session TransferSession
	Files   []TransferFile
}

func (device TrustedDevice) Validate() error {
	if device.Fingerprint == "" {
		return errors.New("device fingerprint must not be empty")
	}
	if device.Alias == "" {
		return errors.New("device alias must not be empty")
	}
	return nil
}

func (session TransferSession) Validate() error {
	if session.ID == "" {
		return errors.New("transfer session ID must not be empty")
	}
	if session.Direction != TransferIncoming && session.Direction != TransferOutgoing {
		return errors.New("invalid transfer direction")
	}
	if !session.Status.Valid() {
		return errors.New("invalid transfer status")
	}
	return nil
}

func (file TransferFile) Validate() error {
	if file.ID == "" {
		return errors.New("transfer file ID must not be empty")
	}
	if file.SessionID == "" {
		return errors.New("transfer file session ID must not be empty")
	}
	if file.FileName == "" {
		return errors.New("transfer file name must not be empty")
	}
	if file.Size < 0 {
		return errors.New("transfer file size must not be negative")
	}
	if file.BytesTransferred < 0 || file.BytesTransferred > file.Size {
		return errors.New("invalid transferred byte count")
	}
	if !file.Status.Valid() {
		return errors.New("invalid file status")
	}
	return nil
}

func (status TransferStatus) Valid() bool {
	switch status {
	case TransferPending, TransferActive, TransferCompleted, TransferFailed, TransferCancelled, TransferRejected:
		return true
	default:
		return false
	}
}

func (status FileStatus) Valid() bool {
	switch status {
	case FilePending, FileActive, FileCompleted, FileFailed, FileCancelled, FileRejected:
		return true
	default:
		return false
	}
}
