// Command devcerts writes a throwaway CA and one certificate per service for
// mutual TLS between the services in docker compose:
//
//	devcerts -out /certs catalog applications timeline gateway waypointctl
//
// See internal/devcerts. Not for production: there, certificates come from a
// private CA (e.g. AWS Private CA) or a service mesh.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/devcerts"
)

func main() {
	out := flag.String("out", "certs", "output directory")
	days := flag.Int("days", 30, "certificate lifetime in days")
	flag.Parse()
	if err := devcerts.Write(*out, flag.Args(), time.Duration(*days)*24*time.Hour, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "devcerts:", err)
		os.Exit(1)
	}
	fmt.Println("certificates ready in", *out)
}
