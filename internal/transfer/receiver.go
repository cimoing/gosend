package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gosend/internal/domain"
	"gosend/internal/localsend"
	"gosend/internal/store"
)

const (
	maximumPrepareBody = 2 << 20
	maximumFileCount   = 1000
	defaultMaximumSize = int64(100 << 30)
	manualDecisionWait = 60 * time.Second
)

type ReceiverConfig struct {
	Directory     string
	Policy        string
	MaxTotalBytes int64
}

type PendingRequest struct {
	ID        string                        `json:"id"`
	Info      localsend.DeviceInfo          `json:"info"`
	Files     map[string]localsend.FileInfo `json:"files"`
	IP        string                        `json:"ip"`
	CreatedAt time.Time                     `json:"createdAt"`
}

type pendingState struct {
	request  PendingRequest
	decision chan bool
}

type receiveFile struct {
	info       localsend.FileInfo
	recordID   string
	token      string
	status     domain.FileStatus
	temporary  string
	targetPath string
}

type receiveSession struct {
	mu        sync.Mutex
	id        string
	peerIP    string
	files     map[string]*receiveFile
	cancelled bool
	cancel    chan struct{}
}

type Receiver struct {
	config   ReceiverConfig
	store    store.Store
	mu       sync.RWMutex
	pending  map[string]*pendingState
	sessions map[string]*receiveSession
}

func NewReceiver(config ReceiverConfig, database store.Store) (*Receiver, error) {
	if database == nil {
		return nil, errors.New("receiver store is required")
	}
	config.Policy = strings.ToLower(strings.TrimSpace(config.Policy))
	switch config.Policy {
	case "manual", "trusted", "auto":
	default:
		return nil, errors.New("invalid receive policy")
	}
	if err := os.MkdirAll(filepath.Join(config.Directory, ".gosend-tmp"), 0o700); err != nil {
		return nil, fmt.Errorf("create receive temporary directory: %w", err)
	}
	if config.MaxTotalBytes <= 0 {
		config.MaxTotalBytes = defaultMaximumSize
	}
	return &Receiver{
		config:   config,
		store:    database,
		pending:  make(map[string]*pendingState),
		sessions: make(map[string]*receiveSession),
	}, nil
}

func (receiver *Receiver) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/localsend/v2/prepare-upload", receiver.handlePrepare)
	mux.HandleFunc("POST /api/localsend/v2/upload", receiver.handleUpload)
	mux.HandleFunc("POST /api/localsend/v2/cancel", receiver.handleCancel)
}

func (receiver *Receiver) Pending() []PendingRequest {
	receiver.mu.RLock()
	defer receiver.mu.RUnlock()
	requests := make([]PendingRequest, 0, len(receiver.pending))
	for _, found := range receiver.pending {
		requests = append(requests, found.request)
	}
	sort.Slice(requests, func(left, right int) bool {
		return requests[left].CreatedAt.Before(requests[right].CreatedAt)
	})
	return requests
}

func (receiver *Receiver) Decide(id string, accept bool) error {
	receiver.mu.RLock()
	found, ok := receiver.pending[id]
	receiver.mu.RUnlock()
	if !ok {
		return store.ErrNotFound
	}
	select {
	case found.decision <- accept:
		return nil
	default:
		return store.ErrConflict
	}
}

func (receiver *Receiver) handlePrepare(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, maximumPrepareBody)
	defer request.Body.Close()
	var prepare localsend.PrepareUploadRequest
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&prepare); err != nil {
		http.Error(response, "invalid body", http.StatusBadRequest)
		return
	}
	if err := validatePrepare(prepare); err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	totalSize := int64(0)
	for _, file := range prepare.Files {
		if file.Size > receiver.config.MaxTotalBytes-totalSize {
			http.Error(response, "transfer is too large", http.StatusBadRequest)
			return
		}
		totalSize += file.Size
	}
	if free, err := availableBytes(receiver.config.Directory); err == nil && totalSize > int64(free) {
		http.Error(response, "insufficient storage", http.StatusInsufficientStorage)
		return
	}
	ip, err := requestIP(request.RemoteAddr)
	if err != nil {
		http.Error(response, "invalid peer address", http.StatusBadRequest)
		return
	}
	accepted, err := receiver.authorize(request.Context(), prepare, ip)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			http.Error(response, "rejected", http.StatusForbidden)
			return
		}
		http.Error(response, "receiver error", http.StatusInternalServerError)
		return
	}
	if !accepted {
		http.Error(response, "rejected", http.StatusForbidden)
		return
	}

	result, err := receiver.createSession(request.Context(), prepare, ip)
	if err != nil {
		http.Error(response, "receiver error", http.StatusInternalServerError)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (receiver *Receiver) authorize(
	ctx context.Context,
	prepare localsend.PrepareUploadRequest,
	ip string,
) (bool, error) {
	switch receiver.config.Policy {
	case "auto":
		return true, nil
	case "trusted":
		devices, err := receiver.store.ListTrustedDevices(ctx)
		if err != nil {
			return false, err
		}
		for _, found := range devices {
			if found.Fingerprint == prepare.Info.Fingerprint {
				return true, nil
			}
		}
		return false, nil
	case "manual":
		id, err := randomID(16)
		if err != nil {
			return false, err
		}
		state := &pendingState{
			request: PendingRequest{
				ID:        id,
				Info:      prepare.Info,
				Files:     prepare.Files,
				IP:        ip,
				CreatedAt: time.Now().UTC(),
			},
			decision: make(chan bool, 1),
		}
		receiver.mu.Lock()
		receiver.pending[id] = state
		receiver.mu.Unlock()
		defer func() {
			receiver.mu.Lock()
			delete(receiver.pending, id)
			receiver.mu.Unlock()
		}()
		timer := time.NewTimer(manualDecisionWait)
		defer timer.Stop()
		select {
		case accepted := <-state.decision:
			return accepted, nil
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, context.DeadlineExceeded
		}
	default:
		return false, errors.New("invalid receive policy")
	}
}

func (receiver *Receiver) createSession(
	ctx context.Context,
	prepare localsend.PrepareUploadRequest,
	ip string,
) (localsend.PrepareUploadResponse, error) {
	sessionID, err := randomID(16)
	if err != nil {
		return localsend.PrepareUploadResponse{}, err
	}
	now := time.Now().UTC()
	session := &receiveSession{
		id:     sessionID,
		peerIP: ip,
		files:  make(map[string]*receiveFile, len(prepare.Files)),
		cancel: make(chan struct{}),
	}
	tokens := make(map[string]string, len(prepare.Files))
	records := make([]domain.TransferFile, 0, len(prepare.Files))
	for protocolID, info := range prepare.Files {
		token, err := randomID(24)
		if err != nil {
			return localsend.PrepareUploadResponse{}, err
		}
		recordID := sessionID + ":" + protocolID
		session.files[protocolID] = &receiveFile{
			info:     info,
			recordID: recordID,
			token:    token,
			status:   domain.FilePending,
		}
		tokens[protocolID] = token
		records = append(records, domain.TransferFile{
			ID:        recordID,
			SessionID: sessionID,
			FileName:  info.FileName,
			Size:      info.Size,
			MIMEType:  info.FileType,
			SHA256:    info.SHA256,
			Status:    domain.FilePending,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	err = receiver.store.CreateTransfer(ctx, domain.TransferSession{
		ID:              sessionID,
		Direction:       domain.TransferIncoming,
		PeerFingerprint: prepare.Info.Fingerprint,
		PeerAlias:       prepare.Info.Alias,
		Status:          domain.TransferPending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, records)
	if err != nil {
		return localsend.PrepareUploadResponse{}, err
	}
	receiver.mu.Lock()
	receiver.sessions[sessionID] = session
	receiver.mu.Unlock()
	return localsend.PrepareUploadResponse{SessionID: sessionID, Files: tokens}, nil
}

func (receiver *Receiver) handleUpload(response http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	fileID := request.URL.Query().Get("fileId")
	token := request.URL.Query().Get("token")
	if sessionID == "" || fileID == "" || token == "" {
		http.Error(response, "missing parameters", http.StatusBadRequest)
		return
	}
	ip, err := requestIP(request.RemoteAddr)
	if err != nil {
		http.Error(response, "invalid peer address", http.StatusBadRequest)
		return
	}
	session, file, status := receiver.beginFile(sessionID, fileID, token, ip)
	if status != http.StatusOK {
		http.Error(response, http.StatusText(status), status)
		return
	}
	if err := receiver.store.UpdateTransferFile(request.Context(), file.recordID, domain.FileActive, 0, ""); err != nil {
		receiver.finishFile(request.Context(), session, file, domain.FileFailed, err.Error())
		http.Error(response, "receiver error", http.StatusInternalServerError)
		return
	}
	_ = receiver.store.UpdateTransferStatus(request.Context(), session.id, domain.TransferActive, "", nil)

	err = receiver.receiveFile(request.Context(), request.Body, session, file)
	if err != nil {
		receiver.finishFile(request.Context(), session, file, domain.FileFailed, err.Error())
		http.Error(response, "file transfer failed", http.StatusInternalServerError)
		return
	}
	receiver.finishFile(request.Context(), session, file, domain.FileCompleted, "")
	response.WriteHeader(http.StatusOK)
}

func (receiver *Receiver) beginFile(sessionID, fileID, token, ip string) (*receiveSession, *receiveFile, int) {
	receiver.mu.RLock()
	session, ok := receiver.sessions[sessionID]
	receiver.mu.RUnlock()
	if !ok {
		return nil, nil, http.StatusForbidden
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	file, ok := session.files[fileID]
	if !ok || session.peerIP != ip || !sameSecret(file.token, token) {
		return nil, nil, http.StatusForbidden
	}
	if file.status != domain.FilePending {
		return nil, nil, http.StatusConflict
	}
	file.status = domain.FileActive
	return session, file, http.StatusOK
}

func (receiver *Receiver) receiveFile(
	ctx context.Context,
	body io.Reader,
	session *receiveSession,
	file *receiveFile,
) error {
	target, err := receiver.availableTarget(file.info.FileName)
	if err != nil {
		return err
	}
	temporaryFile, err := os.CreateTemp(filepath.Join(receiver.config.Directory, ".gosend-tmp"), session.id+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporaryFile.Name()
	file.temporary = temporaryPath
	defer func() {
		_ = temporaryFile.Close()
		if file.targetPath == "" {
			_ = os.Remove(temporaryPath)
		}
	}()

	hasher := sha256.New()
	limited := io.LimitReader(&contextReader{ctx: ctx, cancelled: session.cancel, reader: body}, file.info.Size+1)
	written, err := io.Copy(io.MultiWriter(temporaryFile, hasher), limited)
	if err != nil {
		return err
	}
	if written != file.info.Size {
		return fmt.Errorf("received %d bytes, expected %d", written, file.info.Size)
	}
	if file.info.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), file.info.SHA256) {
		return errors.New("SHA-256 mismatch")
	}
	if err := temporaryFile.Sync(); err != nil {
		return err
	}
	if err := temporaryFile.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, target); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(target)
		return err
	}
	file.targetPath = target
	return nil
}

func (receiver *Receiver) finishFile(
	ctx context.Context,
	session *receiveSession,
	file *receiveFile,
	status domain.FileStatus,
	errorMessage string,
) {
	session.mu.Lock()
	if session.cancelled {
		session.mu.Unlock()
		return
	}
	file.status = status
	bytesTransferred := int64(0)
	if status == domain.FileCompleted {
		bytesTransferred = file.info.Size
	}
	allTerminal := true
	anyFailed := false
	for _, found := range session.files {
		if found.status == domain.FilePending || found.status == domain.FileActive {
			allTerminal = false
		}
		if found.status == domain.FileFailed || found.status == domain.FileCancelled {
			anyFailed = true
		}
	}
	session.mu.Unlock()
	_ = receiver.store.UpdateTransferFile(ctx, file.recordID, status, bytesTransferred, errorMessage)
	if !allTerminal {
		_ = receiver.store.UpdateTransferStatus(ctx, session.id, domain.TransferActive, "", nil)
		return
	}
	completedAt := time.Now().UTC()
	sessionStatus := domain.TransferCompleted
	if anyFailed {
		sessionStatus = domain.TransferFailed
	}
	_ = receiver.store.UpdateTransferStatus(ctx, session.id, sessionStatus, errorMessage, &completedAt)
	receiver.mu.Lock()
	delete(receiver.sessions, session.id)
	receiver.mu.Unlock()
}

func (receiver *Receiver) handleCancel(response http.ResponseWriter, request *http.Request) {
	sessionID := request.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(response, "missing parameters", http.StatusBadRequest)
		return
	}
	ip, err := requestIP(request.RemoteAddr)
	if err != nil {
		http.Error(response, "invalid peer address", http.StatusBadRequest)
		return
	}
	receiver.mu.Lock()
	session, ok := receiver.sessions[sessionID]
	if ok && session.peerIP == ip {
		delete(receiver.sessions, sessionID)
	}
	receiver.mu.Unlock()
	if !ok || session.peerIP != ip {
		http.Error(response, "invalid session", http.StatusForbidden)
		return
	}
	session.mu.Lock()
	session.cancelled = true
	close(session.cancel)
	for _, file := range session.files {
		if file.temporary != "" {
			_ = os.Remove(file.temporary)
		}
		if file.status == domain.FilePending || file.status == domain.FileActive {
			file.status = domain.FileCancelled
			_ = receiver.store.UpdateTransferFile(request.Context(), file.recordID, domain.FileCancelled, 0, "cancelled")
		}
	}
	session.mu.Unlock()
	completedAt := time.Now().UTC()
	_ = receiver.store.UpdateTransferStatus(
		request.Context(),
		session.id,
		domain.TransferCancelled,
		"cancelled",
		&completedAt,
	)
	response.WriteHeader(http.StatusOK)
}

func (receiver *Receiver) availableTarget(name string) (string, error) {
	if !safeFilePath(name) {
		return "", errors.New("unsafe file name")
	}
	relative := filepath.FromSlash(name)
	directory := filepath.Join(receiver.config.Directory, filepath.Dir(relative))
	if err := ensureDirectoryPath(receiver.config.Directory, directory); err != nil {
		return "", err
	}
	fileName := filepath.Base(relative)
	extension := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, extension)
	for index := 0; index < 10000; index++ {
		candidateName := fileName
		if index > 0 {
			candidateName = base + " (" + strconv.Itoa(index) + ")" + extension
		}
		candidate := filepath.Join(directory, candidateName)
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
	}
	return "", errors.New("could not allocate destination file name")
}

func validatePrepare(prepare localsend.PrepareUploadRequest) error {
	if strings.TrimSpace(prepare.Info.Alias) == "" || strings.TrimSpace(prepare.Info.Fingerprint) == "" {
		return errors.New("invalid sender")
	}
	if len(prepare.Files) == 0 || len(prepare.Files) > maximumFileCount {
		return errors.New("invalid file count")
	}
	for protocolID, file := range prepare.Files {
		if protocolID == "" || file.ID != protocolID || file.Size < 0 || !safeFilePath(file.FileName) {
			return errors.New("invalid file metadata")
		}
	}
	return nil
}

func safeFilePath(name string) bool {
	if name == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') || strings.HasPrefix(name, "/") {
		return false
	}
	cleaned := path.Clean(name)
	return cleaned == name && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func ensureDirectoryPath(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("receive directory escaped")
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			if err := os.Mkdir(current, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case statErr != nil:
			return statErr
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			return errors.New("unsafe receive directory")
		}
	}
	return nil
}

func requestIP(address string) (string, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return "", errors.New("invalid IP")
	}
	return ip.String(), nil
}

func randomID(bytesCount int) (string, error) {
	value := make([]byte, bytesCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func sameSecret(expected, actual string) bool {
	if len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

type contextReader struct {
	ctx       context.Context
	cancelled <-chan struct{}
	reader    io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-reader.ctx.Done():
		return 0, reader.ctx.Err()
	case <-reader.cancelled:
		return 0, context.Canceled
	default:
		return reader.reader.Read(buffer)
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
