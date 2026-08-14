// fynanz server: dashboard + API + sync worker in one binary (AGENTS.md §9).
package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"

	"fynanz/internal/metrics"
	"fynanz/internal/store"
	"fynanz/internal/sync"
)

const sandboxDefaultURL = sync.ApiBaseURL

func main() {
	_ = godotenv.Load() // .env is optional; real env is the source of truth
	cfg := loadConfig()

	if cfg.EBAppID == "" || cfg.EBCertPath == "" {
		log.Fatalf("ENABLE_BANKING_APP_ID and ENABLE_BANKING_PRIVATE_KEY must be set (see .env.example)")
	}
	key, err := loadRSAPrivateKey(cfg.EBCertPath)
	if err != nil {
		log.Fatalf("load private key: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	if _, err := st.EnsureDefaultUser(ctx); err != nil {
		log.Fatalf("seed user: %v", err)
	}

	client := sync.NewClient(cfg.EBBaseURL, cfg.EBAppID, key)
	svc := sync.NewService(st, client, metrics.Recorder{}, cfg.EBRedirectURL, cfg.PSUType,
		cfg.HistoryDays, cfg.MaxConsentDays)

	srv := newServer(st, svc)
	go runScheduler(ctx, svc, cfg.SyncInterval)

	addr := ":" + cfg.Port
	log.Printf("fynanz listening on %s (sqlite: %s, eb: %s)", addr, cfg.DBPath, cfg.EBBaseURL)
	if err := http.ListenAndServe(addr, srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}

// runScheduler syncs all active links once at startup, then every interval.
func runScheduler(ctx context.Context, svc *sync.Service, interval time.Duration) {
	run := func() {
		links, err := svc.ActiveLinks(ctx)
		if err != nil {
			log.Printf("scheduler: %v", err)
			return
		}
		for _, l := range links {
			res, err := svc.SyncLink(ctx, l.ID)
			if err != nil {
				log.Printf("scheduled sync %s: %v", l.Bank, err)
				continue
			}
			log.Printf("synced %s: %d accounts, %d new transactions (%d already seen)",
				l.Bank, res.Accounts, res.TransactionsInserted, res.TransactionsSkipped)
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// loadRSAPrivateKey reads the PKCS#1/PKCS#8 RSA private key registered with
// the Enable Banking app (the .pem downloaded at registration).
func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block in private key file")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("PKCS#8 key is not RSA")
	}
	return nil, errors.New("unsupported private key format (want PKCS#1 or PKCS#8 RSA PEM)")
}
