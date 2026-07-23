package app

import (
	"context"
	"testing"

	"gosend/internal/config"
	"gosend/internal/store"
)

func TestRuntimeSettingsPersistAndReload(t *testing.T) {
	database := store.NewMemory()
	t.Cleanup(func() { _ = database.Close() })
	settings, err := loadRuntimeSettings(context.Background(), database, config.Config{
		Alias:         "Original",
		ReceivePolicy: "manual",
	})
	if err != nil {
		t.Fatalf("loadRuntimeSettings() error = %v", err)
	}
	updated, err := settings.Update(context.Background(), database, editableSettings{
		Alias:         "  Home NAS  ",
		DeviceModel:   "Raspberry Pi 5",
		DeviceType:    "Headless",
		ReceivePolicy: "Trusted",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Alias != "Home NAS" || updated.DeviceType != "headless" ||
		updated.ReceivePolicy != "trusted" {
		t.Fatalf("Update() = %+v", updated)
	}
	reloaded, err := loadRuntimeSettings(context.Background(), database, config.Config{
		Alias:         "Ignored",
		ReceivePolicy: "auto",
	})
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if reloaded.Current() != updated {
		t.Fatalf("reloaded = %+v, want %+v", reloaded.Current(), updated)
	}
}

func TestRuntimeSettingsRejectInvalidInput(t *testing.T) {
	for _, input := range []editableSettings{
		{Alias: "", DeviceModel: "GoSend", DeviceType: "server", ReceivePolicy: "manual"},
		{Alias: "NAS", DeviceModel: "", DeviceType: "server", ReceivePolicy: "manual"},
		{Alias: "NAS", DeviceModel: "GoSend", DeviceType: "watch", ReceivePolicy: "manual"},
		{Alias: "NAS", DeviceModel: "GoSend", DeviceType: "server", ReceivePolicy: "sometimes"},
	} {
		if _, err := normalizeEditableSettings(input); err == nil {
			t.Fatalf("normalizeEditableSettings(%+v) error = nil", input)
		}
	}
}
