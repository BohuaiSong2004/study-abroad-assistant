package devcerts

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func serialOf(t *testing.T, path string) string {
	t.Helper()
	c, err := readCert(path)
	if err != nil {
		t.Fatal(err)
	}
	return c.SerialNumber.String()
}

func TestRerunsKeepValidCertificatesAndRenewExpiringOnes(t *testing.T) {
	dir := t.TempDir()
	names := []string{"catalog", "gateway"}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	life := 90 * 24 * time.Hour

	rep, err := Renew(dir, names, life, start)
	if err != nil || !rep.NewCA || len(rep.Issued) != 2 {
		t.Fatalf("first run: %+v %v", rep, err)
	}
	ca, cat := serialOf(t, filepath.Join(dir, "ca.pem")), serialOf(t, filepath.Join(dir, "catalog.pem"))

	// A week later: nothing to do.
	rep, err = Renew(dir, names, life, start.Add(7*24*time.Hour))
	if err != nil || rep.NewCA || len(rep.Issued) != 0 || len(rep.Kept) != 2 {
		t.Fatalf("rerun: %+v %v", rep, err)
	}
	if serialOf(t, filepath.Join(dir, "catalog.pem")) != cat {
		t.Fatal("valid certificate replaced")
	}

	// Two months later (under a third of 90 days left): renewed, same CA.
	rep, err = Renew(dir, names, life, start.Add(65*24*time.Hour))
	if err != nil || rep.NewCA || len(rep.Issued) != 2 {
		t.Fatalf("renewal: %+v %v", rep, err)
	}
	if serialOf(t, filepath.Join(dir, "ca.pem")) != ca || serialOf(t, filepath.Join(dir, "catalog.pem")) == cat {
		t.Fatal("expected a new leaf from the same CA")
	}

	// Long after expiry: still renewed (this is the stack started again
	// after months).
	if _, err := Renew(dir, names, life, start.Add(400*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, _ := readCert(filepath.Join(dir, "gateway.pem"))
	if !c.NotAfter.After(start.Add(400 * 24 * time.Hour)) {
		t.Fatal("expired certificate kept")
	}
}

func TestAnExpiringCAIsReplacedAndEverythingReissued(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if _, err := Renew(dir, []string{"timeline"}, 30*24*time.Hour, start); err != nil {
		t.Fatal(err)
	}
	ca := serialOf(t, filepath.Join(dir, "ca.pem"))
	later := start.Add(CALifetime - 100*24*time.Hour) // under a year left on the CA
	rep, err := Renew(dir, []string{"timeline"}, 30*24*time.Hour, later)
	if err != nil || !rep.NewCA || !slices.Equal(rep.Issued, []string{"timeline"}) {
		t.Fatalf("%+v %v", rep, err)
	}
	if serialOf(t, filepath.Join(dir, "ca.pem")) == ca {
		t.Fatal("CA not replaced")
	}
}

func TestBrokenOrForeignCertificatesAreReissued(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	now := time.Now()
	if _, err := Renew(dir, []string{"a", "b", "c"}, 365*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Renew(other, []string{"c"}, 365*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "a.pem"), []byte("garbage"), 0o644)
	_ = os.Remove(filepath.Join(dir, "b-key.pem"))
	for _, f := range []string{"c.pem", "c-key.pem"} { // signed by another CA
		b, _ := os.ReadFile(filepath.Join(other, f))
		_ = os.WriteFile(filepath.Join(dir, f), b, 0o600)
	}
	rep, err := Renew(dir, []string{"a", "b", "c"}, 365*24*time.Hour, now)
	if err != nil || len(rep.Issued) != 3 {
		t.Fatalf("%+v %v", rep, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}
