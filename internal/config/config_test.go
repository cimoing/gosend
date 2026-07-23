package config

import (
	"path/filepath"
	"testing"
)

func TestParseUsesDataDirectoryForTransferDirectories(t *testing.T) {
	cfg, err := Parse(
		[]string{"--alias", "NAS", "--data-dir", t.TempDir()},
		func(string) (string, bool) { return "", false },
	)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Alias != "NAS" {
		t.Fatalf("Alias = %q, want NAS", cfg.Alias)
	}
	if got, want := cfg.SendDirectory, filepath.Join(cfg.DataDirectory, "send"); got != want {
		t.Fatalf("SendDirectory = %q, want %q", got, want)
	}
	if got, want := cfg.ReceiveDirectory, filepath.Join(cfg.DataDirectory, "receive"); got != want {
		t.Fatalf("ReceiveDirectory = %q, want %q", got, want)
	}
	if got, want := cfg.DatabaseDSN, filepath.Join(cfg.DataDirectory, "gosend.db"); got != want {
		t.Fatalf("DatabaseDSN = %q, want %q", got, want)
	}
}

func TestParseRejectsSameTransferDirectory(t *testing.T) {
	directory := t.TempDir()
	_, err := Parse(
		[]string{"--send-dir", directory, "--receive-dir", directory},
		func(string) (string, bool) { return "", false },
	)
	if err == nil {
		t.Fatal("Parse() error = nil, want validation error")
	}
}

func TestParseReadsEnvironment(t *testing.T) {
	values := map[string]string{
		"GOSEND_ALIAS":           "Raspberry Pi",
		"GOSEND_WEB_ADDRESS":     "127.0.0.1:9090",
		"GOSEND_LOCALSEND_PORT":  "53318",
		"GOSEND_DATA_DIR":        t.TempDir(),
		"GOSEND_DATABASE_DRIVER": "pgsql",
		"GOSEND_DATABASE_DSN":    "postgres://gosend:secret@db/gosend",
	}
	lookup := func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}

	cfg, err := Parse(nil, lookup)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Alias != "Raspberry Pi" || cfg.WebAddress != "127.0.0.1:9090" || cfg.LocalSendPort != 53318 {
		t.Fatalf("Parse() returned unexpected config: %+v", cfg)
	}
	if cfg.DatabaseDriver != "postgres" {
		t.Fatalf("DatabaseDriver = %q, want postgres", cfg.DatabaseDriver)
	}
}

func TestParseRequiresDSNForServerDatabase(t *testing.T) {
	_, err := Parse(
		[]string{"--database-driver", "mysql"},
		func(string) (string, bool) { return "", false },
	)
	if err == nil {
		t.Fatal("Parse() error = nil, want missing DSN error")
	}
}
