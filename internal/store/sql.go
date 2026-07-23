package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gosend/internal/domain"
	"gosend/internal/localsend"
)

type SQL struct {
	database *sql.DB
	dialect  string
}

func (store *SQL) Ping(ctx context.Context) error {
	return store.database.PingContext(ctx)
}

func (store *SQL) Close() error {
	return store.database.Close()
}

func (store *SQL) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := store.database.QueryRowContext(
		ctx,
		store.query("SELECT setting_value FROM settings WHERE setting_key = ?"),
		key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get setting: %w", err)
	}
	return value, nil
}

func (store *SQL) SetSetting(ctx context.Context, key, value string) error {
	now := formatTime(time.Now().UTC())
	var query string
	if store.dialect == "mysql" {
		query = `INSERT INTO settings (setting_key, setting_value, updated_at)
			VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE setting_value = VALUES(setting_value), updated_at = VALUES(updated_at)`
	} else {
		query = `INSERT INTO settings (setting_key, setting_value, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT (setting_key) DO UPDATE SET
				setting_value = excluded.setting_value,
				updated_at = excluded.updated_at`
	}
	if _, err := store.database.ExecContext(ctx, store.query(query), key, value, now); err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

func (store *SQL) UpsertTrustedDevice(ctx context.Context, device domain.TrustedDevice) error {
	device.Fingerprint = localsend.NormalizeFingerprint(device.Fingerprint)
	if err := device.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	if device.UpdatedAt.IsZero() {
		device.UpdatedAt = now
	}

	var query string
	if store.dialect == "mysql" {
		query = `INSERT INTO trusted_devices
			(fingerprint, alias, device_model, device_type, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				alias = VALUES(alias),
				device_model = VALUES(device_model),
				device_type = VALUES(device_type),
				updated_at = VALUES(updated_at)`
	} else {
		query = `INSERT INTO trusted_devices
			(fingerprint, alias, device_model, device_type, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (fingerprint) DO UPDATE SET
				alias = excluded.alias,
				device_model = excluded.device_model,
				device_type = excluded.device_type,
				updated_at = excluded.updated_at`
	}
	_, err := store.database.ExecContext(
		ctx,
		store.query(query),
		device.Fingerprint,
		device.Alias,
		device.DeviceModel,
		device.DeviceType,
		formatTime(device.CreatedAt),
		formatTime(device.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert trusted device: %w", err)
	}
	return nil
}

func (store *SQL) ListTrustedDevices(ctx context.Context) ([]domain.TrustedDevice, error) {
	rows, err := store.database.QueryContext(
		ctx,
		`SELECT fingerprint, alias, device_model, device_type, created_at, updated_at
		 FROM trusted_devices ORDER BY alias, fingerprint`,
	)
	if err != nil {
		return nil, fmt.Errorf("list trusted devices: %w", err)
	}
	defer rows.Close()

	var devices []domain.TrustedDevice
	for rows.Next() {
		var device domain.TrustedDevice
		var createdAt, updatedAt string
		if err := rows.Scan(
			&device.Fingerprint,
			&device.Alias,
			&device.DeviceModel,
			&device.DeviceType,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan trusted device: %w", err)
		}
		if device.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if device.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		device.Fingerprint = localsend.NormalizeFingerprint(device.Fingerprint)
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list trusted devices: %w", err)
	}
	return deduplicateTrustedDevices(devices), nil
}

func (store *SQL) DeleteTrustedDevice(ctx context.Context, fingerprint string) error {
	result, err := store.database.ExecContext(
		ctx,
		store.query("DELETE FROM trusted_devices WHERE LOWER(REPLACE(fingerprint, ':', '')) = ?"),
		localsend.NormalizeFingerprint(fingerprint),
	)
	if err != nil {
		return fmt.Errorf("delete trusted device: %w", err)
	}
	return requireAffected(result)
}

func (store *SQL) CreateTransfer(
	ctx context.Context,
	session domain.TransferSession,
	files []domain.TransferFile,
) error {
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
	for index := range files {
		if files[index].SessionID == "" {
			files[index].SessionID = session.ID
		}
		if files[index].SessionID != session.ID {
			return ErrConflict
		}
		if files[index].CreatedAt.IsZero() {
			files[index].CreatedAt = now
		}
		if files[index].UpdatedAt.IsZero() {
			files[index].UpdatedAt = files[index].CreatedAt
		}
		if err := files[index].Validate(); err != nil {
			return err
		}
	}

	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create transfer: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	var completedAt any
	if session.CompletedAt != nil {
		completedAt = formatTime(*session.CompletedAt)
	}
	_, err = transaction.ExecContext(
		ctx,
		store.query(`INSERT INTO transfer_sessions
			(id, direction, peer_fingerprint, peer_alias, status, error_message, created_at, updated_at, completed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		session.ID,
		session.Direction,
		session.PeerFingerprint,
		session.PeerAlias,
		session.Status,
		session.Error,
		formatTime(session.CreatedAt),
		formatTime(session.UpdatedAt),
		completedAt,
	)
	if err != nil {
		if isConstraintError(err) {
			return ErrConflict
		}
		return fmt.Errorf("insert transfer session: %w", err)
	}

	fileQuery := store.query(`INSERT INTO transfer_files
		(id, session_id, file_name, file_size, mime_type, sha256, status, bytes_transferred, error_message, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	for _, file := range files {
		_, err = transaction.ExecContext(
			ctx,
			fileQuery,
			file.ID,
			file.SessionID,
			file.FileName,
			file.Size,
			file.MIMEType,
			file.SHA256,
			file.Status,
			file.BytesTransferred,
			file.Error,
			formatTime(file.CreatedAt),
			formatTime(file.UpdatedAt),
		)
		if err != nil {
			if isConstraintError(err) {
				return ErrConflict
			}
			return fmt.Errorf("insert transfer file: %w", err)
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit create transfer: %w", err)
	}
	return nil
}

func (store *SQL) GetTransfer(ctx context.Context, id string) (domain.Transfer, error) {
	session, err := store.getTransferSession(ctx, id)
	if err != nil {
		return domain.Transfer{}, err
	}
	rows, err := store.database.QueryContext(
		ctx,
		store.query(`SELECT id, session_id, file_name, file_size, mime_type, sha256, status,
			bytes_transferred, error_message, created_at, updated_at
			FROM transfer_files WHERE session_id = ? ORDER BY created_at, id`),
		id,
	)
	if err != nil {
		return domain.Transfer{}, fmt.Errorf("list transfer files: %w", err)
	}
	defer rows.Close()

	transfer := domain.Transfer{Session: session}
	for rows.Next() {
		file, scanErr := scanTransferFile(rows)
		if scanErr != nil {
			return domain.Transfer{}, scanErr
		}
		transfer.Files = append(transfer.Files, file)
	}
	if err := rows.Err(); err != nil {
		return domain.Transfer{}, fmt.Errorf("list transfer files: %w", err)
	}
	return transfer, nil
}

func (store *SQL) ListTransfers(ctx context.Context, limit int) ([]domain.TransferSession, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := store.database.QueryContext(
		ctx,
		store.query(`SELECT id, direction, peer_fingerprint, peer_alias, status, error_message,
			created_at, updated_at, completed_at
			FROM transfer_sessions ORDER BY created_at DESC, id DESC LIMIT ?`),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list transfers: %w", err)
	}
	defer rows.Close()

	var sessions []domain.TransferSession
	for rows.Next() {
		session, scanErr := scanTransferSession(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list transfers: %w", err)
	}
	return sessions, nil
}

func (store *SQL) DeleteTransfer(ctx context.Context, id string) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete transfer: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(
		ctx,
		store.query("DELETE FROM transfer_files WHERE session_id = ?"),
		id,
	); err != nil {
		return fmt.Errorf("delete transfer files: %w", err)
	}
	result, err := transaction.ExecContext(
		ctx,
		store.query("DELETE FROM transfer_sessions WHERE id = ?"),
		id,
	)
	if err != nil {
		return fmt.Errorf("delete transfer session: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit delete transfer: %w", err)
	}
	return nil
}

func (store *SQL) DeleteTransferFile(ctx context.Context, id string) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete transfer file: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var sessionID string
	err = transaction.QueryRowContext(
		ctx,
		store.query("SELECT session_id FROM transfer_files WHERE id = ?"),
		id,
	).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("find transfer file: %w", err)
	}
	result, err := transaction.ExecContext(
		ctx,
		store.query("DELETE FROM transfer_files WHERE id = ?"),
		id,
	)
	if err != nil {
		return fmt.Errorf("delete transfer file: %w", err)
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	var remaining int
	if err := transaction.QueryRowContext(
		ctx,
		store.query("SELECT COUNT(*) FROM transfer_files WHERE session_id = ?"),
		sessionID,
	).Scan(&remaining); err != nil {
		return fmt.Errorf("count transfer files: %w", err)
	}
	if remaining == 0 {
		if _, err := transaction.ExecContext(
			ctx,
			store.query("DELETE FROM transfer_sessions WHERE id = ?"),
			sessionID,
		); err != nil {
			return fmt.Errorf("delete empty transfer session: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit delete transfer file: %w", err)
	}
	return nil
}

func (store *SQL) ClearTransfers(ctx context.Context) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clear transfers: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, "DELETE FROM transfer_files"); err != nil {
		return fmt.Errorf("clear transfer files: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM transfer_sessions"); err != nil {
		return fmt.Errorf("clear transfer sessions: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit clear transfers: %w", err)
	}
	return nil
}

func (store *SQL) UpdateTransferStatus(
	ctx context.Context,
	id string,
	status domain.TransferStatus,
	errorMessage string,
	completedAt *time.Time,
) error {
	if !status.Valid() {
		return ErrConflict
	}
	var completed any
	if completedAt != nil {
		completed = formatTime(*completedAt)
	}
	result, err := store.database.ExecContext(
		ctx,
		store.query(`UPDATE transfer_sessions
			SET status = ?, error_message = ?, updated_at = ?, completed_at = ?
			WHERE id = ?`),
		status,
		errorMessage,
		formatTime(time.Now().UTC()),
		completed,
		id,
	)
	if err != nil {
		return fmt.Errorf("update transfer status: %w", err)
	}
	return requireAffected(result)
}

func (store *SQL) UpdateTransferFile(
	ctx context.Context,
	id string,
	status domain.FileStatus,
	bytesTransferred int64,
	errorMessage string,
) error {
	if !status.Valid() || bytesTransferred < 0 {
		return ErrConflict
	}
	var fileSize int64
	err := store.database.QueryRowContext(
		ctx,
		store.query("SELECT file_size FROM transfer_files WHERE id = ?"),
		id,
	).Scan(&fileSize)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get transfer file size: %w", err)
	}
	if bytesTransferred > fileSize {
		return ErrConflict
	}
	result, err := store.database.ExecContext(
		ctx,
		store.query(`UPDATE transfer_files
			SET status = ?, bytes_transferred = ?, error_message = ?, updated_at = ?
			WHERE id = ?`),
		status,
		bytesTransferred,
		errorMessage,
		formatTime(time.Now().UTC()),
		id,
	)
	if err != nil {
		return fmt.Errorf("update transfer file: %w", err)
	}
	return requireAffected(result)
}

func (store *SQL) getTransferSession(ctx context.Context, id string) (domain.TransferSession, error) {
	row := store.database.QueryRowContext(
		ctx,
		store.query(`SELECT id, direction, peer_fingerprint, peer_alias, status, error_message,
			created_at, updated_at, completed_at FROM transfer_sessions WHERE id = ?`),
		id,
	)
	session, err := scanTransferSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TransferSession{}, ErrNotFound
	}
	return session, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanTransferSession(row rowScanner) (domain.TransferSession, error) {
	var session domain.TransferSession
	var createdAt, updatedAt string
	var completedAt sql.NullString
	err := row.Scan(
		&session.ID,
		&session.Direction,
		&session.PeerFingerprint,
		&session.PeerAlias,
		&session.Status,
		&session.Error,
		&createdAt,
		&updatedAt,
		&completedAt,
	)
	if err != nil {
		return domain.TransferSession{}, err
	}
	if session.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.TransferSession{}, err
	}
	if session.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.TransferSession{}, err
	}
	if completedAt.Valid {
		value, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return domain.TransferSession{}, parseErr
		}
		session.CompletedAt = &value
	}
	return session, nil
}

func scanTransferFile(row rowScanner) (domain.TransferFile, error) {
	var file domain.TransferFile
	var createdAt, updatedAt string
	err := row.Scan(
		&file.ID,
		&file.SessionID,
		&file.FileName,
		&file.Size,
		&file.MIMEType,
		&file.SHA256,
		&file.Status,
		&file.BytesTransferred,
		&file.Error,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return domain.TransferFile{}, err
	}
	if file.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.TransferFile{}, err
	}
	if file.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.TransferFile{}, err
	}
	return file, nil
}

func (store *SQL) query(query string) string {
	if store.dialect != "postgres" {
		return query
	}
	var builder strings.Builder
	index := 1
	for _, character := range query {
		if character == '?' {
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(index))
			index++
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored time %q: %w", value, err)
	}
	return parsed, nil
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func isConstraintError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") ||
		strings.Contains(message, "duplicate") ||
		strings.Contains(message, "1062") ||
		strings.Contains(message, "23505")
}
