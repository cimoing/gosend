package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const fileName = "identity.pem"

type Identity struct {
	Certificate tls.Certificate
	Fingerprint string
	Path        string
}

func LoadOrCreate(dataDirectory string) (Identity, error) {
	path := filepath.Join(dataDirectory, fileName)
	content, err := os.ReadFile(path)
	if err == nil {
		return parse(content, path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, fmt.Errorf("read identity: %w", err)
	}

	content, err = generate()
	if err != nil {
		return Identity{}, err
	}
	if err := writeAtomic(path, content); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return Identity{}, fmt.Errorf("read concurrently created identity: %w", readErr)
			}
			return parse(existing, path)
		}
		return Identity{}, err
	}
	return parse(content, path)
}

func generate() ([]byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate identity key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}

	now := time.Now().UTC()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "GoSend",
			Organization: []string{"GoSend"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if hostname, hostnameErr := os.Hostname(); hostnameErr == nil && hostname != "" {
		template.DNSNames = append(template.DNSNames, hostname)
	}

	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create identity certificate: %w", err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode identity key: %w", err)
	}

	content := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	content = append(content, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})...)
	return content, nil
}

func parse(content []byte, path string) (Identity, error) {
	certificate, err := tls.X509KeyPair(content, content)
	if err != nil {
		return Identity{}, fmt.Errorf("parse identity %q: %w", path, err)
	}
	if len(certificate.Certificate) == 0 {
		return Identity{}, errors.New("identity contains no certificate")
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return Identity{}, fmt.Errorf("parse identity certificate: %w", err)
	}
	if time.Now().Before(parsed.NotBefore) || time.Now().After(parsed.NotAfter) {
		return Identity{}, errors.New("identity certificate is not currently valid")
	}

	sum := sha256.Sum256(certificate.Certificate[0])
	return Identity{
		Certificate: certificate,
		Fingerprint: hex.EncodeToString(sum[:]),
		Path:        path,
	}, nil
}

func writeAtomic(path string, content []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".identity-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary identity: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set identity permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write identity: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync identity: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close identity: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install identity: %w", err)
	}
	return nil
}
