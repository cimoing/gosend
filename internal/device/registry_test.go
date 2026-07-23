package device

import (
	"testing"
	"time"

	"gosend/internal/localsend"
)

func TestRegistryUpsertAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	registry := NewRegistry(time.Minute)
	registry.now = func() time.Time { return now }

	if !registry.Upsert(localsend.DeviceInfo{
		Alias:       "Phone",
		Fingerprint: "phone-fingerprint",
		Port:        53317,
		Protocol:    "https",
	}, "192.168.1.10") {
		t.Fatal("first Upsert() = false, want new device")
	}
	if registry.Upsert(localsend.DeviceInfo{
		Alias:       "Renamed Phone",
		Fingerprint: "PHONE-FINGERPRINT",
		Port:        53317,
		Protocol:    "https",
	}, "192.168.1.11") {
		t.Fatal("case-variant Upsert() = true, want existing device")
	}
	found, ok := registry.Get("PHONE-FINGERPRINT")
	if !ok || found.Info.Alias != "Renamed Phone" || found.IP != "192.168.1.11" {
		t.Fatalf("Get() = %+v, %v", found, ok)
	}
	if found.Info.Fingerprint != "phone-fingerprint" || len(registry.List()) != 1 {
		t.Fatalf("canonical device = %+v, list = %+v", found, registry.List())
	}

	now = now.Add(time.Minute + time.Nanosecond)
	removed := registry.RemoveExpired()
	if len(removed) != 1 || len(registry.List()) != 0 {
		t.Fatalf("RemoveExpired() = %+v, remaining = %+v", removed, registry.List())
	}
}
