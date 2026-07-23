package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gosend/internal/device"
	"gosend/internal/domain"
	"gosend/internal/localsend"
	"gosend/internal/store"
)

const (
	senderTimeout       = 30 * time.Minute
	maximumParallelSend = 3
)

type SendProgress struct {
	SessionID string                `json:"sessionId"`
	Status    domain.TransferStatus `json:"status"`
	Files     []SendFileProgress    `json:"files"`
}

type SendFileProgress struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Size   int64             `json:"size"`
	Sent   int64             `json:"sent"`
	Status domain.FileStatus `json:"status"`
	Error  string            `json:"error,omitempty"`
}

type sendSource struct {
	protocolID string
	recordID   string
	path       string
	info       localsend.FileInfo
}

type activeSend struct {
	mu       sync.RWMutex
	progress SendProgress
	cancel   context.CancelFunc
}

type Sender struct {
	directory  string
	store      store.Store
	devices    *device.Registry
	self       localsend.DeviceInfo
	mu         sync.RWMutex
	active     map[string]*activeSend
	notifyMu   sync.Mutex
	onChange   func()
	lastNotify time.Time
}

func NewSender(
	directory string,
	database store.Store,
	devices *device.Registry,
	self localsend.DeviceInfo,
) *Sender {
	return &Sender{
		directory: directory,
		store:     database,
		devices:   devices,
		self:      self,
		active:    make(map[string]*activeSend),
	}
}

func (sender *Sender) SetOnChange(onChange func()) {
	sender.notifyMu.Lock()
	sender.onChange = onChange
	sender.notifyMu.Unlock()
}

func (sender *Sender) notifyChange(force bool) {
	sender.notifyMu.Lock()
	if !force && time.Since(sender.lastNotify) < 150*time.Millisecond {
		sender.notifyMu.Unlock()
		return
	}
	sender.lastNotify = time.Now()
	onChange := sender.onChange
	sender.notifyMu.Unlock()
	if onChange != nil {
		onChange()
	}
}

func (sender *Sender) Start(
	parent context.Context,
	fingerprint string,
	names []string,
	pin ...string,
) (string, error) {
	found, ok := sender.devices.Get(fingerprint)
	if !ok {
		return "", errors.New("device is not online")
	}
	sources, sessionID, err := sender.prepareSources(names)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	files := make([]domain.TransferFile, 0, len(sources))
	progress := SendProgress{SessionID: sessionID, Status: domain.TransferPending}
	for _, source := range sources {
		files = append(files, domain.TransferFile{
			ID:        source.recordID,
			SessionID: sessionID,
			FileName:  source.info.FileName,
			Size:      source.info.Size,
			MIMEType:  source.info.FileType,
			SHA256:    source.info.SHA256,
			Status:    domain.FilePending,
			CreatedAt: now,
			UpdatedAt: now,
		})
		progress.Files = append(progress.Files, SendFileProgress{
			ID:     source.protocolID,
			Name:   source.info.FileName,
			Size:   source.info.Size,
			Status: domain.FilePending,
		})
	}
	if err := sender.store.CreateTransfer(parent, domain.TransferSession{
		ID:              sessionID,
		Direction:       domain.TransferOutgoing,
		PeerFingerprint: found.Info.Fingerprint,
		PeerAlias:       found.Info.Alias,
		Status:          domain.TransferPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, files); err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(parent)
	active := &activeSend{progress: progress, cancel: cancel}
	sender.mu.Lock()
	sender.active[sessionID] = active
	sender.mu.Unlock()
	sender.notifyChange(true)
	selectedPIN := ""
	if len(pin) != 0 {
		selectedPIN = pin[0]
	}
	go sender.run(ctx, active, found, sources, selectedPIN)
	return sessionID, nil
}

func (sender *Sender) Cancel(sessionID string) error {
	sender.mu.RLock()
	active, ok := sender.active[sessionID]
	sender.mu.RUnlock()
	if !ok {
		return store.ErrNotFound
	}
	active.cancel()
	return nil
}

func (sender *Sender) CancelAll() {
	sender.mu.RLock()
	active := make([]*activeSend, 0, len(sender.active))
	for _, found := range sender.active {
		active = append(active, found)
	}
	sender.mu.RUnlock()
	for _, found := range active {
		found.cancel()
	}
}

func (sender *Sender) Active() []SendProgress {
	sender.mu.RLock()
	defer sender.mu.RUnlock()
	result := make([]SendProgress, 0, len(sender.active))
	for _, active := range sender.active {
		active.mu.RLock()
		progress := active.progress
		progress.Files = append([]SendFileProgress(nil), progress.Files...)
		active.mu.RUnlock()
		result = append(result, progress)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].SessionID < result[right].SessionID
	})
	return result
}

func (sender *Sender) run(
	ctx context.Context,
	active *activeSend,
	target device.Device,
	sources []sendSource,
	pin string,
) {
	sessionID := active.progress.SessionID
	defer func() {
		sender.mu.Lock()
		delete(sender.active, sessionID)
		sender.mu.Unlock()
		sender.notifyChange(true)
	}()
	sender.setSessionStatus(active, domain.TransferActive)
	_ = sender.store.UpdateTransferStatus(ctx, sessionID, domain.TransferActive, "", nil)

	remoteSession, tokens, client, baseURL, err := sender.prepareRemote(ctx, target, sources, pin)
	if err != nil {
		sender.finishRemainingFiles(active, sources, domain.FileFailed, err.Error())
		sender.finishSession(active, sessionID, domain.TransferFailed, err.Error())
		return
	}
	defer client.CloseIdleConnections()

	semaphore := make(chan struct{}, maximumParallelSend)
	var wait sync.WaitGroup
	var failuresMu sync.Mutex
	var failures []error
	for index := range sources {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				failuresMu.Lock()
				failures = append(failures, ctx.Err())
				failuresMu.Unlock()
				return
			}
			err := sender.uploadOne(ctx, active, index, sources[index], remoteSession, tokens[sources[index].protocolID], client, baseURL)
			if err != nil {
				failuresMu.Lock()
				failures = append(failures, err)
				failuresMu.Unlock()
			}
		}()
	}
	wait.Wait()

	if ctx.Err() != nil {
		_ = sender.cancelRemote(context.Background(), client, baseURL, remoteSession)
		sender.finishRemainingFiles(active, sources, domain.FileCancelled, "cancelled")
		sender.finishSession(active, sessionID, domain.TransferCancelled, "cancelled")
		return
	}
	if len(failures) != 0 {
		_ = sender.cancelRemote(context.Background(), client, baseURL, remoteSession)
		sender.finishRemainingFiles(active, sources, domain.FileFailed, failures[0].Error())
		sender.finishSession(active, sessionID, domain.TransferFailed, failures[0].Error())
		return
	}
	sender.finishSession(active, sessionID, domain.TransferCompleted, "")
}

func (sender *Sender) prepareSources(names []string) ([]sendSource, string, error) {
	if len(names) == 0 || len(names) > maximumFileCount {
		return nil, "", errors.New("invalid file count")
	}
	sessionID, err := randomID(16)
	if err != nil {
		return nil, "", err
	}
	root, err := filepath.EvalSymlinks(sender.directory)
	if err != nil {
		return nil, "", err
	}
	sources := make([]sendSource, 0, len(names))
	seen := make(map[string]struct{})
	for _, name := range names {
		if _, exists := seen[name]; exists {
			return nil, "", errors.New("duplicate source file")
		}
		seen[name] = struct{}{}
		path, err := secureSource(root, name)
		if err != nil {
			return nil, "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, "", err
		}
		if !info.Mode().IsRegular() {
			return nil, "", errors.New("source is not a regular file")
		}
		hash, err := hashFile(path)
		if err != nil {
			return nil, "", err
		}
		protocolID, err := randomID(12)
		if err != nil {
			return nil, "", err
		}
		contentType := mime.TypeByExtension(filepath.Ext(info.Name()))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		sources = append(sources, sendSource{
			protocolID: protocolID,
			recordID:   sessionID + ":" + protocolID,
			path:       path,
			info: localsend.FileInfo{
				ID:       protocolID,
				FileName: filepath.ToSlash(filepath.Clean(name)),
				Size:     info.Size(),
				FileType: contentType,
				SHA256:   hash,
				Metadata: &localsend.FileMetadata{Modified: info.ModTime().UTC().Format(time.RFC3339Nano)},
			},
		})
	}
	return sources, sessionID, nil
}

func (sender *Sender) prepareRemote(
	ctx context.Context,
	target device.Device,
	sources []sendSource,
	pin string,
) (string, map[string]string, *http.Client, string, error) {
	client, err := localsend.HTTPClient(target.Info.Protocol, target.Info.Fingerprint, senderTimeout)
	if err != nil {
		return "", nil, nil, "", err
	}
	baseURL := target.Info.Protocol + "://" + net.JoinHostPort(target.IP, strconv.Itoa(target.Info.Port))
	files := make(map[string]localsend.FileInfo, len(sources))
	for _, source := range sources {
		files[source.protocolID] = source.info
	}
	body, _ := json.Marshal(localsend.PrepareUploadRequest{Info: sender.self, Files: files})
	prepareURL := baseURL + "/api/localsend/v2/prepare-upload"
	if pin != "" {
		prepareURL += "?pin=" + url.QueryEscape(pin)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, prepareURL, bytes.NewReader(body))
	if err != nil {
		client.CloseIdleConnections()
		return "", nil, nil, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		client.CloseIdleConnections()
		return "", nil, nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		client.CloseIdleConnections()
		return "", nil, nil, "", fmt.Errorf("prepare upload returned HTTP %d", response.StatusCode)
	}
	var prepared localsend.PrepareUploadResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumPrepareBody)).Decode(&prepared); err != nil {
		client.CloseIdleConnections()
		return "", nil, nil, "", err
	}
	if prepared.SessionID == "" || len(prepared.Files) != len(sources) {
		client.CloseIdleConnections()
		return "", nil, nil, "", errors.New("invalid prepare upload response")
	}
	return prepared.SessionID, prepared.Files, client, baseURL, nil
}

func (sender *Sender) uploadOne(
	ctx context.Context,
	active *activeSend,
	index int,
	source sendSource,
	remoteSession, token string,
	client *http.Client,
	baseURL string,
) error {
	if token == "" {
		return errors.New("receiver omitted file token")
	}
	file, err := os.Open(source.path)
	if err != nil {
		return err
	}
	defer file.Close()
	sender.setFile(active, index, domain.FileActive, 0, "")
	_ = sender.store.UpdateTransferFile(ctx, source.recordID, domain.FileActive, 0, "")
	reader := &sendProgressReader{reader: file, update: func(sent int64) {
		sender.setFile(active, index, domain.FileActive, sent, "")
	}}
	url := baseURL + "/api/localsend/v2/upload?sessionId=" + remoteSession +
		"&fileId=" + source.protocolID + "&token=" + token
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reader)
	if err != nil {
		return err
	}
	request.ContentLength = source.info.Size
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		sender.setFile(active, index, domain.FileFailed, reader.sent, err.Error())
		_ = sender.store.UpdateTransferFile(context.Background(), source.recordID, domain.FileFailed, reader.sent, err.Error())
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err = fmt.Errorf("upload returned HTTP %d", response.StatusCode)
		sender.setFile(active, index, domain.FileFailed, reader.sent, err.Error())
		_ = sender.store.UpdateTransferFile(context.Background(), source.recordID, domain.FileFailed, reader.sent, err.Error())
		return err
	}
	sender.setFile(active, index, domain.FileCompleted, source.info.Size, "")
	return sender.store.UpdateTransferFile(context.Background(), source.recordID, domain.FileCompleted, source.info.Size, "")
}

func (sender *Sender) finishSession(active *activeSend, id string, status domain.TransferStatus, message string) {
	sender.setSessionStatus(active, status)
	completedAt := time.Now().UTC()
	_ = sender.store.UpdateTransferStatus(context.Background(), id, status, message, &completedAt)
}

func (sender *Sender) finishRemainingFiles(
	active *activeSend,
	sources []sendSource,
	status domain.FileStatus,
	message string,
) {
	active.mu.Lock()
	type update struct {
		recordID string
		sent     int64
	}
	var updates []update
	for index := range active.progress.Files {
		found := &active.progress.Files[index]
		if found.Status == domain.FileCompleted || found.Status == domain.FileFailed || found.Status == domain.FileCancelled {
			continue
		}
		found.Status = status
		found.Error = message
		updates = append(updates, update{recordID: sources[index].recordID, sent: found.Sent})
	}
	active.mu.Unlock()
	for _, found := range updates {
		_ = sender.store.UpdateTransferFile(context.Background(), found.recordID, status, found.sent, message)
	}
	sender.notifyChange(true)
}

func (sender *Sender) setSessionStatus(active *activeSend, status domain.TransferStatus) {
	active.mu.Lock()
	active.progress.Status = status
	active.mu.Unlock()
	sender.notifyChange(true)
}

func (sender *Sender) setFile(active *activeSend, index int, status domain.FileStatus, sent int64, message string) {
	active.mu.Lock()
	active.progress.Files[index].Status = status
	active.progress.Files[index].Sent = sent
	active.progress.Files[index].Error = message
	active.mu.Unlock()
	sender.notifyChange(status != domain.FileActive)
}

func (sender *Sender) cancelRemote(ctx context.Context, client *http.Client, baseURL, sessionID string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/localsend/v2/cancel?sessionId="+sessionID, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return nil
}

func secureSource(root, name string) (string, error) {
	if filepath.IsAbs(name) || name == "" {
		return "", errors.New("invalid source path")
	}
	candidate := filepath.Join(root, filepath.Clean(name))
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("source path escapes send directory")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	resolvedRelative, err := filepath.Rel(root, resolved)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return "", errors.New("source symlink escapes send directory")
	}
	return resolved, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type sendProgressReader struct {
	reader io.Reader
	sent   int64
	update func(int64)
}

func (reader *sendProgressReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.sent += int64(count)
	reader.update(reader.sent)
	return count, err
}
