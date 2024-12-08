// Package devcerts writes a throwaway CA and one certificate per service for
// mutual TLS between the services in docker compose (and tests).
//
// It writes ca.pem and <name>.pem/<name>-key.pem. Each certificate is valid
// for both server and client use, for the DNS name <name> (the compose
// service name) and localhost. Running it again keeps what is still good and
// renews the rest: a certificate that is missing, unreadable, not signed by
// the current CA or within a third of its lifetime of expiring is reissued,
// and an expiring CA is replaced (which reissues every certificate). Compose
// runs it on every `up`, so a stack started again after weeks gets fresh
// certificates instead of failing every TLS handshake. Not for production:
// there, certificates come from a private CA (e.g. AWS Private CA) or a
// service mesh.
package devcerts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// CALifetime is the lifetime of a newly created CA.
const CALifetime = 10 * 365 * 24 * time.Hour

// Report says what Write did.
type Report struct {
	NewCA   bool
	Issued  []string // certificates (re)issued
	Kept    []string // certificates still valid and kept
	Expires time.Time
}

// Write creates or renews dir's CA and the certificates for names, each
// valid for life from now.
func Write(dir string, names []string, life time.Duration, now time.Time) error {
	_, err := Renew(dir, names, life, now)
	return err
}

// Renew is Write with a report of what changed.
func Renew(dir string, names []string, life time.Duration, now time.Time) (Report, error) {
	var rep Report
	if len(names) == 0 {
		return rep, errors.New("name at least one service")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return rep, err
	}
	caCert, caKey, fresh, err := loadOrCreateCA(dir, now)
	if err != nil {
		return rep, err
	}
	rep.NewCA = fresh
	for _, n := range names {
		certPath, keyPath := filepath.Join(dir, n+".pem"), filepath.Join(dir, n+"-key.pem")
		if !fresh && stillGood(certPath, keyPath, n, caCert, life, now) {
			rep.Kept = append(rep.Kept, n)
			continue
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return rep, err
		}
		tmpl := &x509.Certificate{
			SerialNumber: serial(),
			Subject:      pkix.Name{CommonName: n, Organization: []string{"Waypoint dev"}},
			DNSNames:     []string{n, "localhost"},
			IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(life),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			return rep, err
		}
		// Key first, then certificate: running services reload when the
		// certificate file changes, and must find the matching key by then.
		if err := writeKey(keyPath, key); err != nil {
			return rep, err
		}
		if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
			return rep, err
		}
		rep.Issued = append(rep.Issued, n)
	}
	rep.Expires = caCert.NotAfter
	return rep, nil
}

// stillGood reports whether an existing certificate can be kept: it parses,
// matches its key, is signed by the current CA, names the service, and has
// more than a third of its lifetime left.
func stillGood(certPath, keyPath, name string, ca *x509.Certificate, life time.Duration, now time.Time) bool {
	cert, err := readCert(certPath)
	if err != nil {
		return false
	}
	kb, err := os.ReadFile(keyPath) //nolint:gosec // operator-chosen output directory
	if err != nil {
		return false
	}
	kp, _ := pem.Decode(kb)
	if kp == nil {
		return false
	}
	key, err := x509.ParseECPrivateKey(kp.Bytes)
	if err != nil || !key.PublicKey.Equal(cert.PublicKey) {
		return false
	}
	if cert.CheckSignatureFrom(ca) != nil || !slices.Contains(cert.DNSNames, name) {
		return false
	}
	return now.After(cert.NotBefore) && cert.NotAfter.Sub(now) > life/3
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-chosen output directory
	if err != nil {
		return nil, err
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	return x509.ParseCertificate(p.Bytes)
}

// loadOrCreateCA reuses the CA while it has more than a year left, and
// otherwise creates a new one (fresh=true).
func loadOrCreateCA(dir string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, bool, error) {
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca-key.pem")
	if cert, err := readCert(certPath); err == nil && cert.NotAfter.Sub(now) > 365*24*time.Hour && now.After(cert.NotBefore) {
		if kb, err := os.ReadFile(keyPath); err == nil { //nolint:gosec // operator-chosen output directory
			if kp, _ := pem.Decode(kb); kp != nil {
				if key, err := x509.ParseECPrivateKey(kp.Bytes); err == nil && key.PublicKey.Equal(cert.PublicKey) {
					return cert, key, false, nil
				}
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Waypoint dev CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CALifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, false, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return nil, nil, false, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, false, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, true, err
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

func writeKey(path string, k *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	return writePEM(path, "EC PRIVATE KEY", der, 0o600)
}

// writePEM replaces path atomically (temporary file + rename), so a reader
// never sees a half-written file.
func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
