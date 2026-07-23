package localsend

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func HTTPClient(protocol, fingerprint string, timeout time.Duration) (*http.Client, error) {
	if protocol != "http" && protocol != "https" {
		return nil, errors.New("unsupported LocalSend protocol")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if protocol == "https" {
		expected := strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
		if len(expected) != sha256.Size*2 {
			return nil, errors.New("invalid LocalSend certificate fingerprint")
		}
		transport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // The self-signed certificate is pinned below.
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("peer presented no certificate")
				}
				sum := sha256.Sum256(state.PeerCertificates[0].Raw)
				if hex.EncodeToString(sum[:]) != expected {
					return fmt.Errorf("certificate fingerprint mismatch")
				}
				return nil
			},
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
