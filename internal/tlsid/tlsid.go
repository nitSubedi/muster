// Package tlsid gives each agent a persistent self-signed TLS identity.
// Trust in it comes from the cluster key (ADR-0005), not from a CA.
package tlsid

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
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Identity is a node's certificate and its fingerprint.
type Identity struct {
	Cert tls.Certificate
	FP   string
}

// LoadOrCreate reads node.crt/node.key from dir, generating them on first
// use. The key file is 0600.
func LoadOrCreate(dir string) (Identity, error) {
	certPath, keyPath := filepath.Join(dir, "node.crt"), filepath.Join(dir, "node.key")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		if err := generate(certPath, keyPath); err != nil {
			return Identity{}, err
		}
		cert, err = tls.LoadX509KeyPair(certPath, keyPath)
	}
	if err != nil {
		return Identity{}, err
	}
	return Identity{Cert: cert, FP: Fingerprint(cert.Certificate[0])}, nil
}

// ServerConfig is the TLS config agents serve with.
func (id Identity) ServerConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{id.Cert}, MinVersion: tls.VersionTLS13}
}

// Fingerprint is the hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// PeerFP returns the fingerprint of the certificate presented on a
// connection, or "" for plain connections.
func PeerFP(cs *tls.ConnectionState) string {
	if cs == nil || len(cs.PeerCertificates) == 0 {
		return ""
	}
	return Fingerprint(cs.PeerCertificates[0].Raw)
}

func generate(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "muster-node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
