package discovery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gosend/internal/device"
	"gosend/internal/localsend"
)

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
		Fingerprint: "peer-fingerprint",
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
