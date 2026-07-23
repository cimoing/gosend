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
	"net/netip"
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
	scanInterval       = 60 * time.Second
	scanRequestTimeout = 750 * time.Millisecond
	scanTimeout        = 20 * time.Second
	maximumScanHosts   = 1024
	maximumScanWorkers = 64
)

type Config struct {
	Alias            string
	Port             int
	Fingerprint      string
	Certificate      tls.Certificate
	AnnounceInterval time.Duration
	RegisterRoutes   func(*http.ServeMux)
}

type Service struct {
	config   Config
	registry *device.Registry
	logger   *slog.Logger
	server   *http.Server
	packet   *ipv4.PacketConn
	sendMu   sync.Mutex
	scanMu   sync.Mutex
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
	config.Fingerprint = localsend.NormalizeFingerprint(config.Fingerprint)
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
	if service.config.RegisterRoutes != nil {
		service.config.RegisterRoutes(mux)
	}
	return mux
}

func (service *Service) Run(ctx context.Context) error {
	listener, err := listenLocalSendTCP(service.server.Addr)
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
	service.startScan(runContext)

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

func listenLocalSendTCP(address string) (net.Listener, error) {
	// LocalSend discovery uses an IPv4 multicast group, so peers connect back
	// over IPv4. An unspecified "tcp" listener can become IPv6-only on hosts
	// with net.ipv6.bindv6only enabled while still showing as :::port.
	return net.Listen("tcp4", address)
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
	info.Fingerprint = localsend.NormalizeFingerprint(info.Fingerprint)
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
		info.Fingerprint = localsend.NormalizeFingerprint(info.Fingerprint)
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
	announced.Fingerprint = localsend.NormalizeFingerprint(announced.Fingerprint)
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
	client, err := localsend.HTTPClient(protocol, announced.Fingerprint, requestTimeout)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
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
	scanTicker := time.NewTicker(scanInterval)
	defer announceTicker.Stop()
	defer cleanupTicker.Stop()
	defer scanTicker.Stop()
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
		case <-scanTicker.C:
			service.startScan(ctx)
		}
	}
}

// TriggerScan starts the LocalSend HTTP fallback discovery in the background.
// It returns false when another scan is already running.
func (service *Service) TriggerScan() bool {
	return service.startScan(context.Background())
}

func (service *Service) startScan(parent context.Context) bool {
	if !service.scanMu.TryLock() {
		return false
	}
	go func() {
		defer service.scanMu.Unlock()
		ctx, cancel := context.WithTimeout(parent, scanTimeout)
		defer cancel()
		found, scanned, err := service.scan(ctx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			service.logger.Debug("HTTP fallback discovery failed", "error", err)
			return
		}
		if found > 0 {
			service.logger.Info("HTTP fallback discovery found devices", "found", found, "scanned", scanned)
		} else {
			service.logger.Debug("HTTP fallback discovery completed", "scanned", scanned)
		}
	}()
	return true
}

func (service *Service) scan(ctx context.Context) (int, int, error) {
	addresses, err := localScanAddresses()
	if err != nil {
		return 0, 0, err
	}
	type result struct {
		found bool
	}
	jobs := make(chan netip.Addr)
	results := make(chan result)
	workers := maximumScanWorkers
	if len(addresses) < workers {
		workers = len(addresses)
	}
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for address := range jobs {
				found := service.probePeer(ctx, address.String())
				select {
				case results <- result{found: found}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, address := range addresses {
			select {
			case jobs <- address:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(results)
	}()

	found := 0
	scanned := 0
	for result := range results {
		scanned++
		if result.found {
			found++
		}
	}
	return found, scanned, ctx.Err()
}

func (service *Service) probePeer(parent context.Context, ip string) bool {
	for _, protocol := range []string{"https", "http"} {
		ctx, cancel := context.WithTimeout(parent, scanRequestTimeout)
		info, err := service.registerAddress(ctx, protocol, ip)
		cancel()
		if err != nil {
			continue
		}
		if localsend.NormalizeFingerprint(info.Fingerprint) == service.config.Fingerprint {
			return false
		}
		return service.registry.Upsert(info, ip)
	}
	return false
}

func (service *Service) registerAddress(
	ctx context.Context,
	protocol string,
	ip string,
) (localsend.DeviceInfo, error) {
	body, err := json.Marshal(service.SelfInfo(false))
	if err != nil {
		return localsend.DeviceInfo{}, err
	}
	url := protocol + "://" + net.JoinHostPort(ip, strconv.Itoa(service.config.Port)) +
		"/api/localsend/v2/register"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return localsend.DeviceInfo{}, err
	}
	request.Header.Set("Content-Type", "application/json")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	var certificateFingerprint string
	if protocol == "https" {
		transport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // Discovery learns and pins the certificate fingerprint below.
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("peer presented no certificate")
				}
				sum := sha256.Sum256(state.PeerCertificates[0].Raw)
				certificateFingerprint = hex.EncodeToString(sum[:])
				return nil
			},
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   scanRequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects are not allowed during discovery")
		},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return localsend.DeviceInfo{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return localsend.DeviceInfo{}, fmt.Errorf("register returned HTTP %d", response.StatusCode)
	}
	var info localsend.DeviceInfo
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumMessageSize))
	if err := decoder.Decode(&info); err != nil {
		return localsend.DeviceInfo{}, err
	}
	if protocol == "https" {
		info.Fingerprint = certificateFingerprint
	}
	if info.Port == 0 {
		info.Port = service.config.Port
	}
	if info.Protocol == "" {
		info.Protocol = protocol
	}
	if !validPeer(info) {
		return localsend.DeviceInfo{}, errors.New("invalid register response")
	}
	return info, nil
}

func localScanAddresses() ([]netip.Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	local := make(map[netip.Addr]struct{})
	prefixes := make(map[netip.Prefix]struct{})
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 ||
			virtualScanInterface(networkInterface.Name) {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() {
				continue
			}
			local[prefix.Addr()] = struct{}{}
			bits := prefix.Bits()
			if bits < 24 {
				bits = 24
			}
			if bits >= 31 {
				continue
			}
			prefixes[netip.PrefixFrom(prefix.Addr(), bits).Masked()] = struct{}{}
		}
	}

	result := make([]netip.Addr, 0)
	for prefix := range prefixes {
		for address := prefix.Addr().Next(); prefix.Contains(address); address = address.Next() {
			if len(result) >= maximumScanHosts {
				return result, nil
			}
			if _, isLocal := local[address]; isLocal {
				continue
			}
			next := address.Next()
			if !prefix.Contains(next) {
				break
			}
			result = append(result, address)
		}
	}
	return result, nil
}

func virtualScanInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"docker", "br-", "veth", "cni", "flannel"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
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
