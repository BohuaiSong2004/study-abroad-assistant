// Package testutil provides DynamoDB Local fixtures for integration tests.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
)

// Endpoint returns the DynamoDB Local endpoint for tests. Integration tests
// are skipped when DYNAMODB_ENDPOINT is unset, unless REQUIRE_DYNAMODB=1
// (CI), in which case they fail instead of silently skipping.
func Endpoint(t testing.TB) string {
	t.Helper()
	ep := os.Getenv("DYNAMODB_ENDPOINT")
	if ep == "" {
		if os.Getenv("REQUIRE_DYNAMODB") == "1" {
			t.Fatal("DYNAMODB_ENDPOINT is required (REQUIRE_DYNAMODB=1)")
		}
		t.Skip("DYNAMODB_ENDPOINT not set; skipping DynamoDB integration test")
	}
	return ep
}

// Tables creates a fresh set of tables with a unique prefix and returns
// clients configured with the given cache mode. Tables are dropped on cleanup.
func Tables(t testing.TB, cacheMode string) (*ddb.Clients, string) {
	t.Helper()
	ep := Endpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := ddb.Connect(ctx, ddb.Config{Endpoint: ep, Region: "us-east-1", CacheMode: cacheMode})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "t" + strings.ToLower(ids.New("")[14:]) + "-"
	if err := ddb.EnsureTables(ctx, c.Raw, prefix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ddb.DeleteTables(ctx, c.Raw, prefix)
	})
	return c, prefix
}

// Logger returns a logger that writes to the test log when -v, else discards.
func Logger(t testing.TB) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Eventually polls cond until it returns true or the timeout elapses.
func Eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: "+msg, append([]any{timeout}, args...)...)
}

// InternalToken is the shared secret used by in-process test servers.
const InternalToken = "test-internal-token"
