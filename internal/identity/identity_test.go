package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreatePersistsFingerprint(t *testing.T) {
	directory := t.TempDir()

	first, err := LoadOrCreate(directory)
	if err != nil {
		t.Fatalf("LoadOrCreate(first) error = %v", err)
	}
	second, err := LoadOrCreate(directory)
	if err != nil {
		t.Fatalf("LoadOrCreate(second) error = %v", err)
	}

	if first.Fingerprint == "" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprints = %q and %q, want same non-empty value", first.Fingerprint, second.Fingerprint)
	}
	info, err := os.Stat(first.Path)
	if err != nil {
		t.Fatalf("Stat(identity) error = %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("identity file is empty")
	}
}

func TestLoadOrCreateRejectsInvalidIdentity(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "identity.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadOrCreate(directory); err == nil {
		t.Fatal("LoadOrCreate() error = nil, want invalid identity error")
	}
}
