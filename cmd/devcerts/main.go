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
	days := flag.Int("days", 365, "certificate lifetime in days (renewed when a third is left)")
	flag.Parse()
	rep, err := devcerts.Renew(*out, flag.Args(), time.Duration(*days)*24*time.Hour, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "devcerts:", err)
		os.Exit(1)
	}
	if rep.NewCA {
		fmt.Println("created a new CA (services must restart to trust it; compose starts them after this job)")
	}
	fmt.Printf("certificates ready in %s: issued %v, kept %v\n", *out, rep.Issued, rep.Kept)
}
