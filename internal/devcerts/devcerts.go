// Package devcerts writes a throwaway CA and one certificate per service for
// mutual TLS between the services in docker compose (and tests).
//
// It writes ca.pem and <name>.pem/<name>-key.pem. Each certificate is valid
// for both server and client use, for the DNS name <name> (the compose
// service name) and localhost. Existing files are kept, so restarts reuse
// the same CA. Not for production: there, certificates come from a private
// CA (e.g. AWS Private CA) or a service mesh.
package devcerts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Write creates (or completes) dir with ca.pem, ca-key.pem and a
// certificate and key per name.
func Write(dir string, names []string, life time.Duration, now time.Time) error {
	if len(names) == 0 {
		return errors.New("name at least one service")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	caCert, caKey, err := loadOrCreateCA(dir, life, now)
	if err != nil {
		return err
	}
	for _, n := range names {
		if exists(filepath.Join(dir, n+".pem")) && exists(filepath.Join(dir, n+"-key.pem")) {
			continue
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
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
			return err
		}
		if err := writeKey(filepath.Join(dir, n+"-key.pem"), key); err != nil {
			return err
		}
		if err := writePEM(filepath.Join(dir, n+".pem"), "CERTIFICATE", der, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func loadOrCreateCA(dir string, life time.Duration, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca-key.pem")
	if exists(certPath) && exists(keyPath) {
		cb, err := os.ReadFile(certPath) //nolint:gosec // operator-chosen output directory
		if err != nil {
			return nil, nil, err
		}
		kb, err := os.ReadFile(keyPath) //nolint:gosec // operator-chosen output directory
		if err != nil {
			return nil, nil, err
		}
		cp, _ := pem.Decode(cb)
		kp, _ := pem.Decode(kb)
		if cp == nil || kp == nil {
			return nil, nil, errors.New("unreadable CA files")
		}
		cert, err := x509.ParseCertificate(cp.Bytes)
		if err != nil {
			return nil, nil, err
		}
		key, err := x509.ParseECPrivateKey(kp.Bytes)
		return cert, key, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Waypoint dev CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(life),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return nil, nil, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
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

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
