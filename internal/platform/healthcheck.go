package platform

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// HealthcheckCommand implements "<binary> healthcheck": it probes this
// process's own readiness endpoint and exits 0/1. Container images are
// distroless (no shell, curl or wget), so the binary is its own probe.
func HealthcheckCommand() {
	if len(os.Args) < 2 || os.Args[1] != "healthcheck" {
		return
	}
	os.Exit(Healthcheck(Env("ADMIN_ADDR", ":9100"), os.Stderr))
}

// Healthcheck probes addr's /readyz and returns the process exit code.
func Healthcheck(addr string, stderr io.Writer) int {
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/readyz", nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "unhealthy:", err)
		return 1
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "unhealthy:", err)
		return 1
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintln(stderr, "unhealthy: status", res.StatusCode)
		return 1
	}
	return 0
}
