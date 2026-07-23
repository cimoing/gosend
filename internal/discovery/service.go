package discovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"gosend/internal/device"
	"gosend/internal/localsend"
)

const (
	maximumMessageSize = 64 << 10
	requestTimeout     = 4 * time.Second
	cleanupInterval    = 10 * time.Second
)

type Config struct {
	Alias            string
	Port             int
	Fingerprint      string
	Certificate      tls.Certificate
	AnnounceInterval time.Duration
}

type Service struct {
	config   Config
	registry *device.Registry
	logger   *slog.Logger
	server   *http.Server
	packet   *ipv4.PacketConn
	sendMu   sync.Mutex
	register chan struct{}
}

func New(config Config, registry *device.Registry, logger *slog.Logger) *Service {
	if config.AnnounceInterval <= 0 {
		config.AnnounceInterval = 30 * time.Second
	}
	if registry == nil {
		registry = device.NewRegistry(0)
	}
	if logger == nil {
		logger = slog.Default()
	}
	service := &Service{
		config:   config,
		registry: registry,
		logger:   logger,
		register: make(chan struct{}, 8),
	}
	service.server = &http.Server{
		Addr:              ":" + strconv.Itoa(config.Port),
		Handler:           service.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{config.Certificate},
		},
	}
	return service
}

func (service *Service) SelfInfo(announce bool) localsend.DeviceInfo {
	return localsend.DeviceInfo{
		Alias:       service.config.Alias,
		Version:     localsend.ProtocolVersion,
		DeviceModel: "GoSend",
		DeviceType:  "server",
		Fingerprint: service.config.Fingerprint,
		Port:        service.config.Port,
		Protocol:    "https",
		Download:    false,
		Announce:    announce,
	}
}

func (service *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/localsend/v2/register", service.handleRegister)
	mux.HandleFunc("GET /api/localsend/v2/info", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, service.SelfInfo(false))
	})
	return mux
}

func (service *Service) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", service.server.Addr)
	if err != nil {
		return fmt.Errorf("listen for LocalSend HTTPS: %w", err)
	}
	tlsListener := tls.NewListener(listener, service.server.TLSConfig)

	packet, err := listenMulticast(service.config.Port)
	if err != nil {
		_ = listener.Close()
		return err
	}
	service.packet = packet

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsChannel := make(chan error, 3)
	go func() { errorsChannel <- normalizeServerError(service.server.Serve(tlsListener)) }()
	go func() { errorsChannel <- service.receive(runContext) }()
	go func() { errorsChannel <- service.periodic(runContext) }()

	service.logger.Info("LocalSend discovery started", "port", service.config.Port)
	if err := service.announce(true); err != nil {
		service.logger.Warn("initial multicast announcement failed", "error", err)
	}

	var runErr error
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case runErr = <-errorsChannel:
	}
	cancel()
	_ = packet.Close()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := service.server.Shutdown(shutdownContext); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down LocalSend HTTPS: %w", err)
	}
	return runErr
}

func (service *Service) handleRegister(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, maximumMessageSize)
	defer request.Body.Close()
	var info localsend.DeviceInfo
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&info); err != nil {
		http.Error(response, "invalid body", http.StatusBadRequest)
		return
	}
	if err := ensureJSONEnd(decoder); err != nil || !validPeer(info) {
		http.Error(response, "invalid body", http.StatusBadRequest)
		return
	}
	if info.Fingerprint != service.config.Fingerprint {
		if ip, err := remoteIP(request.RemoteAddr); err == nil {
			service.registry.Upsert(info, ip)
		}
	}
	writeJSON(response, http.StatusOK, service.SelfInfo(false))
}

func (service *Service) receive(ctx context.Context) error {
	buffer := make([]byte, maximumMessageSize)
	for {
		if err := service.packet.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		count, _, source, err := service.packet.ReadFrom(buffer)
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					continue
				}
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return ctx.Err()
			}
			return fmt.Errorf("read multicast: %w", err)
		}
		var info localsend.DeviceInfo
		if err := json.Unmarshal(buffer[:count], &info); err != nil || !validPeer(info) {
			continue
		}
		if info.Fingerprint == service.config.Fingerprint {
			continue
		}
		ip, err := remoteIP(source.String())
		if err != nil {
			continue
		}
		service.registry.Upsert(info, ip)
		if info.Announce {
			service.startRegister(ctx, info, ip)
		}
	}
}

func (service *Service) startRegister(ctx context.Context, info localsend.DeviceInfo, ip string) {
	select {
	case service.register <- struct{}{}:
		go func() {
			defer func() { <-service.register }()
			if err := service.registerPeer(ctx, info, ip); err != nil {
				service.logger.Debug("HTTP registration failed; using multicast fallback", "peer", info.Alias, "error", err)
				if fallbackErr := service.announce(false); fallbackErr != nil {
					service.logger.Debug("multicast response failed", "error", fallbackErr)
				}
			}
		}()
	default:
		service.logger.Debug("registration queue is full", "peer", info.Alias)
	}
}

func (service *Service) registerPeer(parent context.Context, announced localsend.DeviceInfo, ip string) error {
	protocol := strings.ToLower(announced.Protocol)
	if protocol != "http" && protocol != "https" {
		return errors.New("unsupported peer protocol")
	}
	if announced.Port < 1 || announced.Port > 65535 {
		return errors.New("invalid peer port")
	}
	address := net.JoinHostPort(ip, strconv.Itoa(announced.Port))
	url := protocol + "://" + address + "/api/localsend/v2/register"
	body, err := json.Marshal(service.SelfInfo(false))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	transport := peerTransport(protocol, announced.Fingerprint)
	client := &http.Client{Transport: transport, Timeout: requestTimeout}
	defer transport.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("register returned HTTP %d", response.StatusCode)
	}
	var returned localsend.DeviceInfo
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumMessageSize))
	if err := decoder.Decode(&returned); err != nil {
		return err
	}
	returned.Fingerprint = announced.Fingerprint
	if returned.Port == 0 {
		returned.Port = announced.Port
	}
	if returned.Protocol == "" {
		returned.Protocol = announced.Protocol
	}
	if !validPeer(returned) {
		return errors.New("invalid register response")
	}
	service.registry.Upsert(returned, ip)
	return nil
}

func (service *Service) periodic(ctx context.Context) error {
	announceTicker := time.NewTicker(service.config.AnnounceInterval)
	cleanupTicker := time.NewTicker(cleanupInterval)
	defer announceTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-announceTicker.C:
			if err := service.announce(true); err != nil {
				service.logger.Debug("multicast announcement failed", "error", err)
			}
		case <-cleanupTicker.C:
			for _, expired := range service.registry.RemoveExpired() {
				service.logger.Debug("device expired", "alias", expired.Info.Alias, "fingerprint", expired.Info.Fingerprint)
			}
		}
	}
}

func (service *Service) announce(announce bool) error {
	payload, err := json.Marshal(service.SelfInfo(announce))
	if err != nil {
		return err
	}
	group := &net.UDPAddr{IP: net.ParseIP(localsend.DefaultMulticastIP), Port: service.config.Port}
	interfaces, err := multicastInterfaces()
	if err != nil {
		return err
	}
	service.sendMu.Lock()
	defer service.sendMu.Unlock()
	var sent int
	for index := range interfaces {
		if err := service.packet.SetMulticastInterface(&interfaces[index]); err != nil {
			continue
		}
		if _, err := service.packet.WriteTo(payload, nil, group); err == nil {
			sent++
		}
	}
	if sent == 0 {
		return errors.New("no multicast interface accepted the announcement")
	}
	return nil
}

func listenMulticast(port int) (*ipv4.PacketConn, error) {
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen for LocalSend multicast: %w", err)
	}
	packet := ipv4.NewPacketConn(connection)
	group := &net.UDPAddr{IP: net.ParseIP(localsend.DefaultMulticastIP)}
	interfaces, err := multicastInterfaces()
	if err != nil {
		_ = packet.Close()
		return nil, err
	}
	var joined int
	for index := range interfaces {
		if err := packet.JoinGroup(&interfaces[index], group); err == nil {
			joined++
		}
	}
	if joined == 0 {
		_ = packet.Close()
		return nil, errors.New("no multicast-capable interface could join the LocalSend group")
	}
	if err := packet.SetMulticastTTL(1); err != nil {
		_ = packet.Close()
		return nil, err
	}
	_ = packet.SetMulticastLoopback(false)
	return packet, nil
}

func multicastInterfaces() ([]net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	var capable []net.Interface
	for _, networkInterface := range interfaces {
		required := net.FlagUp | net.FlagMulticast
		if networkInterface.Flags&required != required || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		capable = append(capable, networkInterface)
	}
	if len(capable) == 0 {
		return nil, errors.New("no active multicast-capable network interface")
	}
	return capable, nil
}

func peerTransport(protocol, fingerprint string) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if protocol != "https" {
		return transport
	}
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // Verified against the LocalSend certificate fingerprint below.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("peer presented no certificate")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			actual := hex.EncodeToString(sum[:])
			expected := strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
			if actual != expected {
				return fmt.Errorf("certificate fingerprint mismatch")
			}
			return nil
		},
	}
	return transport
}

func validPeer(info localsend.DeviceInfo) bool {
	if strings.TrimSpace(info.Alias) == "" || strings.TrimSpace(info.Fingerprint) == "" {
		return false
	}
	if info.Version == "" || !strings.HasPrefix(info.Version, "2.") {
		return false
	}
	if info.Port < 1 || info.Port > 65535 {
		return false
	}
	return info.Protocol == "http" || info.Protocol == "https"
}

func remoteIP(address string) (string, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return "", errors.New("remote address is not an IP")
	}
	return ip.String(), nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func normalizeServerError(err error) error {
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
