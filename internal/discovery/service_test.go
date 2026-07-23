package discovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"gosend/internal/device"
	"gosend/internal/localsend"
)

func TestLocalSendTCPListenerAcceptsIPv4(t *testing.T) {
	listener, err := listenLocalSendTCP(":0")
	if err != nil {
		t.Fatalf("listenLocalSendTCP() error = %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			acceptErr = connection.Close()
		}
		accepted <- acceptErr
	}()

	connection, err := net.DialTimeout(
		"tcp4",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		time.Second,
	)
	if err != nil {
		t.Fatalf("IPv4 dial error = %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close IPv4 connection: %v", err)
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("accept IPv4 connection: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out accepting IPv4 connection")
	}
}

func TestRegisterAndInfoHandlers(t *testing.T) {
	registry := device.NewRegistry(0)
	service := New(Config{
		Alias:       "GoSend NAS",
		Port:        53317,
		Fingerprint: "self-fingerprint",
	}, registry, nil)
	peer := localsend.DeviceInfo{
		Alias:       "Phone",
		Version:     "2.0",
		DeviceType:  "mobile",
		Fingerprint: "PEER-FINGERPRINT",
		Port:        53317,
		Protocol:    "https",
	}
	body, _ := json.Marshal(peer)
	request := httptest.NewRequest(http.MethodPost, "/api/localsend/v2/register", bytes.NewReader(body))
	request.RemoteAddr = "192.168.1.20:45678"
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("register status = %d, body = %s", response.Code, response.Body.String())
	}
	found, ok := registry.Get(peer.Fingerprint)
	if !ok || found.IP != "192.168.1.20" {
		t.Fatalf("registered device = %+v, %v", found, ok)
	}

	infoRequest := httptest.NewRequest(http.MethodGet, "/api/localsend/v2/info", nil)
	infoResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(infoResponse, infoRequest)
	var self localsend.DeviceInfo
	if err := json.NewDecoder(infoResponse.Body).Decode(&self); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if self.Alias != "GoSend NAS" || self.Fingerprint != "self-fingerprint" || self.Announce {
		t.Fatalf("info = %+v", self)
	}
	service.UpdateInfo("Renamed NAS", "Raspberry Pi 5", "headless")
	updated := service.SelfInfo(false)
	if updated.Alias != "Renamed NAS" || updated.DeviceModel != "Raspberry Pi 5" ||
		updated.DeviceType != "headless" {
		t.Fatalf("updated info = %+v", updated)
	}
	devices := registry.List()
	if len(devices) != 1 || devices[0].Info.Fingerprint != "peer-fingerprint" {
		t.Fatalf("registered devices = %+v", devices)
	}
}

func TestRegisterRejectsInvalidBody(t *testing.T) {
	service := New(Config{Alias: "GoSend", Port: 53317, Fingerprint: "self"}, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/localsend/v2/register", bytes.NewBufferString(`{"alias":"missing fields"}`))
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestServerTimeoutsAllowManualApprovalAndLargeTransfers(t *testing.T) {
	service := New(Config{Alias: "GoSend", Port: 53317, Fingerprint: "self"}, nil, nil)
	if service.server.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout = %v, want no whole-upload deadline", service.server.ReadTimeout)
	}
	if service.server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want no manual-approval deadline", service.server.WriteTimeout)
	}
	if service.server.ReadHeaderTimeout == 0 {
		t.Fatal("ReadHeaderTimeout = 0, want slow-header protection")
	}
}

func TestRegisterAddressLearnsHTTPSFingerprintAndResponseDefaults(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/localsend/v2/register" {
			http.NotFound(response, request)
			return
		}
		writeJSON(response, http.StatusOK, localsend.DeviceInfo{
			Alias:       "Nearby Phone",
			Version:     localsend.ProtocolVersion,
			Fingerprint: "ignored-for-https",
		})
	}))
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	service := New(Config{
		Alias:       "GoSend",
		Port:        port,
		Fingerprint: "self",
	}, device.NewRegistry(0), nil)
	info, err := service.registerAddress(context.Background(), "https", "127.0.0.1")
	if err != nil {
		t.Fatalf("registerAddress() error = %v", err)
	}
	sum := sha256.Sum256(server.Certificate().Raw)
	wantFingerprint := hex.EncodeToString(sum[:])
	if info.Fingerprint != wantFingerprint || info.Port != port || info.Protocol != "https" {
		t.Fatalf("registerAddress() = %+v", info)
	}
}
