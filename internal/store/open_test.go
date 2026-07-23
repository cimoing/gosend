package store

import (
	"context"
	"path/filepath"
	"testing"

	"gosend/internal/domain"
)

func TestNormalizeDriverAliases(t *testing.T) {
	for input, want := range map[string]string{
		"memory":     "memory",
		"mem":        "memory",
		"sqlite3":    "sqlite",
		"mariadb":    "mysql",
		"pgsql":      "postgres",
		"postgresql": "postgres",
	} {
		got, err := NormalizeDriver(input)
		if err != nil || got != want {
			t.Errorf("NormalizeDriver(%q) = %q, %v; want %q, nil", input, got, err, want)
		}
	}
}

func TestSQLiteMigrationsCanReopen(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "gosend.db"),
	}
	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	if err := first.SetSetting(ctx, "persistent", "yes"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}
	if err := first.UpsertTrustedDevice(ctx, domain.TrustedDevice{
		Fingerprint: "persistent-fingerprint",
		Alias:       "Persistent Device",
	}); err != nil {
		t.Fatalf("UpsertTrustedDevice() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}

	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}
	defer func() { _ = second.Close() }()
	value, err := second.GetSetting(ctx, "persistent")
	if err != nil || value != "yes" {
		t.Fatalf("GetSetting() = %q, %v; want yes, nil", value, err)
	}
	devices, err := second.ListTrustedDevices(ctx)
	if err != nil || len(devices) != 1 || devices[0].Fingerprint != "persistent-fingerprint" {
		t.Fatalf("ListTrustedDevices() = %+v, %v", devices, err)
	}
}
