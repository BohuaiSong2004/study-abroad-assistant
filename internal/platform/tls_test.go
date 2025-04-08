package platform

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/devcerts"
)

func files(dir, name string) TLSFiles {
	return TLSFiles{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, name+".pem"), Key: filepath.Join(dir, name+"-key.pem")}
}

// mtlsServer starts a health-only gRPC server with the "catalog" identity.
func mtlsServer(t *testing.T, dir string) string {
	t.Helper()
	creds, err := files(dir, "catalog").ServerCredentials()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewGRPCServer(NewLogger("test"), "", grpc.Creds(creds))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Server.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func healthCheck(target string, creds credentials.TransportCredentials, extra ...grpc.DialOption) error {
	cc, err := grpc.NewClient(target, append(DialOptions("", 2*time.Second, creds), extra...)...)
	if err != nil {
		return err
	}
	defer func() { _ = cc.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(false))
	return err
}

func TestMutualTLSAcceptsOnlyCertificatesFromTheCA(t *testing.T) {
	ours, theirs := t.TempDir(), t.TempDir()
	if err := devcerts.Write(ours, []string{"catalog", "gateway"}, 24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := devcerts.Write(theirs, []string{"intruder"}, 24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	addr := mtlsServer(t, ours)
	_, port, _ := net.SplitHostPort(addr)

	gw, err := files(ours, "gateway").ClientCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err := healthCheck("localhost:"+port, gw); err != nil {
		t.Fatalf("gateway with a CA-signed certificate: %v", err)
	}
	if err := healthCheck(addr, nil); err == nil {
		t.Fatal("plaintext client accepted")
	}
	// A certificate from another CA is refused by the server...
	intruder := files(theirs, "intruder")
	intruder.CA = filepath.Join(ours, "ca.pem") // trusts our server, but its own cert is foreign
	ic, err := intruder.ClientCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err := healthCheck("localhost:"+port, ic); err == nil {
		t.Fatal("client certificate from a foreign CA accepted")
	}
	// ...and a client that trusts another CA refuses our server.
	wrongTrust, err := files(theirs, "intruder").ClientCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err := healthCheck("localhost:"+port, wrongTrust); err == nil {
		t.Fatal("client accepted a server outside its CA")
	}
	// The server name is verified: the same listener reached under a name
	// the catalog certificate does not carry is refused.
	if err := healthCheck("localhost:"+port, gw, grpc.WithAuthority("billing")); err == nil {
		t.Fatal("server certificate accepted for a name it does not carry")
	}
	if err := healthCheck("localhost:"+port, gw, grpc.WithAuthority("catalog")); err != nil {
		t.Fatalf("server reached by its service name: %v", err)
	}
}

func TestPartialTLSConfigurationIsAnError(t *testing.T) {
	f := TLSFiles{CA: "ca.pem"}
	if !f.Enabled() {
		t.Fatal("partial config must count as enabled, never fall back to plaintext")
	}
	if _, err := f.ServerCredentials(); err == nil {
		t.Fatal("server accepted a partial configuration")
	}
	if _, err := f.ClientCredentials(); err == nil {
		t.Fatal("client accepted a partial configuration")
	}
}

func TestRenewedCertificateIsPickedUpWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := devcerts.Write(dir, []string{"svc"}, 24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	f := files(dir, "svc")
	kp, err := newKeyPair(f.Cert, f.Key)
	if err != nil {
		t.Fatal(err)
	}
	serial := func() string {
		c, err := kp.get()
		if err != nil {
			t.Fatal(err)
		}
		x, _ := x509.ParseCertificate(c.Certificate[0])
		return x.SerialNumber.String()
	}
	before := serial()
	// Renew: remove the leaf and let devcerts issue a new one from the same CA.
	_ = os.Remove(f.Cert)
	_ = os.Remove(f.Key)
	if err := devcerts.Write(dir, []string{"svc"}, 24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(f.Cert, future, future)
	if serial() != before {
		t.Fatal("re-read before the check interval")
	}
	kp.checked = time.Time{} // the check interval has passed
	if serial() == before {
		t.Fatal("renewed certificate not picked up")
	}
	// A broken file mid-rotation keeps the loaded certificate.
	if err := os.WriteFile(f.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(f.Cert, future.Add(time.Minute), future.Add(time.Minute))
	kp.checked = time.Time{}
	if _, err := kp.get(); err != nil {
		t.Fatalf("half-written renewal broke the server: %v", err)
	}
}
