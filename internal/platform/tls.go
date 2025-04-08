package platform

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
)

// TLSFiles locates the mutual-TLS material for internal gRPC: the CA that
// signs every service certificate, and this process's certificate and key.
// With all three set, servers only accept clients presenting a certificate
// from the CA, and clients verify the server's name against it; the
// internal token is still checked on top (defence in depth).
type TLSFiles struct {
	CA, Cert, Key string
}

// TLSFromEnv reads GRPC_TLS_CA, GRPC_TLS_CERT and GRPC_TLS_KEY.
func TLSFromEnv() TLSFiles {
	return TLSFiles{CA: Env("GRPC_TLS_CA", ""), Cert: Env("GRPC_TLS_CERT", ""), Key: Env("GRPC_TLS_KEY", "")}
}

// Enabled reports whether mutual TLS is configured. A partial configuration
// is an error from ServerCredentials/ClientCredentials, never a silent
// fallback to plaintext.
func (f TLSFiles) Enabled() bool { return f.CA != "" || f.Cert != "" || f.Key != "" }

func (f TLSFiles) check() error {
	if f.CA == "" || f.Cert == "" || f.Key == "" {
		return errors.New("mutual TLS needs GRPC_TLS_CA, GRPC_TLS_CERT and GRPC_TLS_KEY")
	}
	return nil
}

func (f TLSFiles) pool() (*x509.CertPool, error) {
	pem, err := os.ReadFile(f.CA)
	if err != nil {
		return nil, err
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s: no certificates", f.CA)
	}
	return p, nil
}

// ServerCredentials requires and verifies client certificates.
func (f TLSFiles) ServerCredentials() (credentials.TransportCredentials, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	pool, err := f.pool()
	if err != nil {
		return nil, err
	}
	kp, err := newKeyPair(f.Cert, f.Key)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:     tls.VersionTLS13,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      pool,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return kp.get() },
	}), nil
}

// ClientCredentials presents this process's certificate and verifies the
// server against the CA (host name from the dial target).
func (f TLSFiles) ClientCredentials() (credentials.TransportCredentials, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	pool, err := f.pool()
	if err != nil {
		return nil, err
	}
	kp, err := newKeyPair(f.Cert, f.Key)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:           tls.VersionTLS13,
		RootCAs:              pool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return kp.get() },
	}), nil
}

// keyPair re-reads the certificate when its file changes, so a renewed
// certificate is picked up by new connections without a restart. (A new CA
// still needs one.)
type keyPair struct {
	cert, key string
	mu        sync.Mutex
	mod       time.Time
	checked   time.Time
	cur       *tls.Certificate
}

func newKeyPair(cert, key string) (*keyPair, error) {
	k := &keyPair{cert: cert, key: key}
	if _, err := k.get(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *keyPair) get() (*tls.Certificate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cur != nil && time.Since(k.checked) < 10*time.Second {
		return k.cur, nil
	}
	k.checked = time.Now()
	st, err := os.Stat(k.cert)
	if err != nil {
		if k.cur != nil {
			return k.cur, nil // keep serving with the loaded one
		}
		return nil, err
	}
	if k.cur != nil && st.ModTime().Equal(k.mod) {
		return k.cur, nil
	}
	c, err := tls.LoadX509KeyPair(k.cert, k.key)
	if err != nil {
		if k.cur != nil {
			return k.cur, nil // mid-rotation (key not written yet): retry later
		}
		return nil, err
	}
	k.cur, k.mod = &c, st.ModTime()
	return k.cur, nil
}
