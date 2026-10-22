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
	"fmt"
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

// LoadOrCreate reads dir/node.pem (certificate and key together),
// generating it on first use. The pair lives in one file written by atomic
// rename, so two processes starting at once can never leave a key that
// doesn't match its certificate. The file is 0600.
func LoadOrCreate(dir string) (Identity, error) {
	path := filepath.Join(dir, "node.pem")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := generate(path); err != nil {
			return Identity{}, err
		}
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return Identity{}, err
	}
	cert, err := tls.X509KeyPair(b, b)
	if err != nil {
		return Identity{}, fmt.Errorf("%s: %w", path, err)
	}
	os.Chmod(path, 0o600)
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

func generate(path string) error {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".node-*.pem")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Link fails if another process won the race; then we use its pair.
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}
