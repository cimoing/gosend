// Package localsend contains protocol-facing types for LocalSend Protocol v2.1.
package localsend

import "strings"

const (
	SpecificationVersion = "2.1"
	ProtocolVersion      = "2.0"
	DefaultPort          = 53317
	DefaultMulticastIP   = "224.0.0.167"
	DefaultMulticastPort = 53317
)

type DeviceInfo struct {
	Alias       string `json:"alias"`
	Version     string `json:"version"`
	DeviceModel string `json:"deviceModel,omitempty"`
	DeviceType  string `json:"deviceType,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Port        int    `json:"port,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Download    bool   `json:"download,omitempty"`
	Announce    bool   `json:"announce,omitempty"`
}

type FileInfo struct {
	ID       string        `json:"id"`
	FileName string        `json:"fileName"`
	Size     int64         `json:"size"`
	FileType string        `json:"fileType"`
	SHA256   string        `json:"sha256,omitempty"`
	Preview  string        `json:"preview,omitempty"`
	Metadata *FileMetadata `json:"metadata,omitempty"`
}

type FileMetadata struct {
	Modified string `json:"modified,omitempty"`
	Accessed string `json:"accessed,omitempty"`
}

type PrepareUploadRequest struct {
	Info  DeviceInfo          `json:"info"`
	Files map[string]FileInfo `json:"files"`
}

type PrepareUploadResponse struct {
	SessionID string            `json:"sessionId"`
	Files     map[string]string `json:"files"`
}

// NormalizeFingerprint returns the canonical representation used for device
// identity comparisons and storage. LocalSend peers may advertise the same
// hexadecimal certificate fingerprint with different casing or separators.
func NormalizeFingerprint(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
}
