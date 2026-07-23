package localsend

import "testing"

func TestNormalizeFingerprint(t *testing.T) {
	if got := NormalizeFingerprint(" AA:bb:CC "); got != "aabbcc" {
		t.Fatalf("NormalizeFingerprint() = %q, want aabbcc", got)
	}
}
