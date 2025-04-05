package platform

import (
	"context"
	"fmt"
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
	addr := Env("ADMIN_ADDR", ":9100")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/readyz", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "unhealthy:", err)
		os.Exit(1)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintln(os.Stderr, "unhealthy: status", res.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
