package sfu

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
)

// DTLSManager handles DTLS-SRTP certificate lifecycle and fingerprint
// management for WebRTC peer connections. It generates and caches certificates
// to avoid expensive key generation on every peer connection.
type DTLSManager struct {
	// certificate is the cached DTLS certificate used for peer connections.
	certificate *webrtc.Certificate
	// fingerprint is the SHA-256 fingerprint of the certificate.
	fingerprint string
	// expiresAt tracks when the certificate expires.
	expiresAt time.Time
	// certLifetime is how long each generated certificate is valid.
	certLifetime time.Duration

	// mu protects the certificate and fingerprint.
	mu sync.RWMutex
}

// NewDTLSManager creates a DTLS manager that generates and caches certificates
// with the specified lifetime duration.
func NewDTLSManager(certLifetime time.Duration) (*DTLSManager, error) {
	dm := &DTLSManager{
		certLifetime: certLifetime,
	}
	if err := dm.regenerateCertificate(); err != nil {
		return nil, fmt.Errorf("dtls: initial certificate generation: %w", err)
	}
	return dm, nil
}

// GetCertificate returns the current DTLS certificate, regenerating it if
// it has expired. Thread-safe.
func (dm *DTLSManager) GetCertificate() (*webrtc.Certificate, error) {
	dm.mu.RLock()
	if dm.certificate != nil && time.Now().Before(dm.expiresAt) {
		cert := dm.certificate
		dm.mu.RUnlock()
		return cert, nil
	}
	dm.mu.RUnlock()

	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Double-check after acquiring write lock
	if dm.certificate != nil && time.Now().Before(dm.expiresAt) {
		return dm.certificate, nil
	}

	if err := dm.regenerateCertificate(); err != nil {
		return nil, fmt.Errorf("dtls: regenerate certificate: %w", err)
	}
	return dm.certificate, nil
}

// GetFingerprint returns the SHA-256 fingerprint of the current certificate.
func (dm *DTLSManager) GetFingerprint() string {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.fingerprint
}

// regenerateCertificate generates a new ECDSA certificate for DTLS.
// Must be called with dm.mu held for writing.
func (dm *DTLSManager) regenerateCertificate() error {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ecdsa key: %w", err)
	}

	now := time.Now()
	notAfter := now.Add(dm.certLifetime)

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "nakama-voice-dtls",
		},
		NotBefore:             now,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	x509Cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	cert := webrtc.CertificateFromX509(privateKey, x509Cert)
	dm.certificate = &cert
	dm.expiresAt = notAfter
	dm.fingerprint = computeSHA256Fingerprint(certDER)

	return nil
}

// computeSHA256Fingerprint computes the SHA-256 fingerprint of DER-encoded cert data.
func computeSHA256Fingerprint(der []byte) string {
	hash := crypto.SHA256.New()
	hash.Write(der)
	digest := hash.Sum(nil)
	hexStr := hex.EncodeToString(digest)

	// Format as colon-separated pairs
	result := make([]byte, 0, len(hexStr)+len(hexStr)/2)
	for i := 0; i < len(hexStr); i += 2 {
		if i > 0 {
			result = append(result, ':')
		}
		result = append(result, hexStr[i], hexStr[i+1])
	}
	return string(result)
}

// CertificateFromX509 wraps pion's CertificateFromX509 for use outside
// this package. This exists to keep DTLS certificate handling centralised.
func CertificateFromX509(key crypto.PrivateKey, cert *x509.Certificate) webrtc.Certificate {
	return webrtc.CertificateFromX509(key, cert)
}