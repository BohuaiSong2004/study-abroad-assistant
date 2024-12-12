// Package platform holds the cross-cutting pieces every binary shares:
// configuration, logging, gRPC server/client setup, the internal auth
// contract between the gateway and services, and the admin HTTP server.
package platform

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Env returns the environment variable or def when unset/empty.
func Env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// EnvInt returns an integer environment variable or def.
func EnvInt(key string, def int) int {
	if v, err := strconv.Atoi(Env(key, "")); err == nil {
		return v
	}
	return def
}

// EnvBool returns a boolean environment variable or def.
func EnvBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(Env(key, "")); err == nil {
		return v
	}
	return def
}

// EnvDuration returns a duration environment variable (e.g. "30s") or def.
func EnvDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(Env(key, "")); err == nil {
		return v
	}
	return def
}
