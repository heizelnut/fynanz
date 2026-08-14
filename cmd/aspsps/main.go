// aspsps lists the banks the Enable Banking API currently exposes for a
// country — the authoritative (name, country) pairs used when connecting a
// link. Useful before wiring a new bank (the names differ from docs/memory):
//
//	go run ./cmd/aspsps [country ...]
//	e.g. go run ./cmd/aspsps LT IT GB
//
// Prints name, country, PSU types and the maximum consent validity in days.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"fynanz/internal/sync"
)

func main() {
	_ = godotenv.Load()
	appID := os.Getenv("ENABLE_BANKING_APP_ID")
	keyPath := os.Getenv("ENABLE_BANKING_PRIVATE_KEY")
	if appID == "" || keyPath == "" {
		log.Fatal("set ENABLE_BANKING_APP_ID and ENABLE_BANKING_PRIVATE_KEY (cp .env.example .env)")
	}
	key, err := loadKey(keyPath)
	if err != nil {
		log.Fatal(err)
	}
	countries := os.Args[1:]
	if len(countries) == 0 {
		countries = []string{""} // empty = all
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := sync.NewClient(sync.ApiBaseURL, appID, key)

	for _, country := range countries {
		if country == "" {
			country = "all"
		}
		aspsps, err := client.ListASPSPs(ctx, country)
		if err != nil {
			log.Fatalf("%s: %v", country, err)
		}
		fmt.Printf("== %s (%d) ==\n", country, len(aspsps))
		for _, a := range aspsps {
			days := ""
			if a.MaxConsentValidity != nil {
				days = fmt.Sprintf("%dd", *a.MaxConsentValidity/86400)
			}
			psu := strings.Join(a.PSUTypes, "/")
			fmt.Printf("  %-28s %-3s  psu=%s  consent=%s\n", a.Name, a.Country, psu, days)
		}
	}
}
