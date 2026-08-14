// fynanz server: dashboard + API + sync worker in one binary (AGENTS.md §9).
package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"

	"fynanz/internal/metrics"
	"fynanz/internal/notify"
	"fynanz/internal/store"
	"fynanz/internal/sync"
)

const sandboxDefaultURL = sync.ApiBaseURL

func main() {
	_ = godotenv.Load() // .env is optional; real env is the source of truth
	cfg := loadConfig()

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

	// Credentials resolve per request from Settings (DB) > env — so the
	// production app can be configured completely from the UI after deploy;
	// the server also boots without credentials so the Settings page can
	// provide them (previously a startup fatal).
	client := sync.NewDynamicClient(bankResolve(st, cfg))
	notifier := notify.NewDispatcher(buildNotifyLoader(st))
	metrics.RegisterFinanceCollector(st)
	redirectURL := cfg.PublicBaseURL + "/links/callback"
	svc := sync.NewService(st, client, metrics.Recorder{}, notifier, cfg.PublicBaseURL,
		redirectURL, cfg.PSUType, cfg.HistoryDays, cfg.MaxConsentDays)

	// Backfill categorization for rows synced before rules existed (§5).
	if err := svc.Reclassify(ctx); err != nil {
		log.Printf("reclassify: %v", err)
	}

	srv := newServer(st, svc, cfg)
	go runScheduler(ctx, svc, cfg.SyncInterval)

	addr := ":" + cfg.Port
	log.Printf("fynanz listening on %s (sqlite: %s, eb: %s)", addr, cfg.DBPath, cfg.EBBaseURL)
	if err := http.ListenAndServe(addr, srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}

// bankResolve returns Enable Banking credentials with precedence
// Settings (DB) > environment; errors surface as API failures (e.g. during
// connect) with a pointer to the Settings page.
func bankResolve(st *store.Store, cfg Config) sync.CredentialResolver {
	ctx := context.Background()
	return func() (sync.Credentials, error) {
		userID, err := st.EnsureDefaultUser(ctx)
		if err != nil {
			return sync.Credentials{}, err
		}
		dbGet := func(k string) string {
			v, _ := st.GetSetting(ctx, userID, k)
			return v
		}
		appID := firstNonEmpty(dbGet(store.SettingEBAppID), cfg.EBAppID)
		baseURL := firstNonEmpty(dbGet(store.SettingEBBaseURL), cfg.EBBaseURL)

		var key *rsa.PrivateKey
		if pemText := dbGet(store.SettingEBPrivateKey); pemText != "" {
			k, err := sync.ParseRSAPrivateKey([]byte(pemText))
			if err != nil {
				return sync.Credentials{}, fmt.Errorf("stored Enable Banking private key is invalid: %w", err)
			}
			key = k
		} else if cfg.EBCertPath != "" {
			k, err := loadRSAPrivateKey(cfg.EBCertPath)
			if err == nil {
				key = k
			}
		}

		if appID == "" || baseURL == "" || key == nil {
			return sync.Credentials{}, errors.New("enable Banking credentials are not configured — add them in Settings → Enable Banking")
		}
		return sync.Credentials{BaseURL: baseURL, AppID: appID, PrivateKey: key}, nil
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// buildNotifyLoader serves channel configs from the store for the notifier
// (§7: per-user configuration, plain DB rows).
func buildNotifyLoader(st *store.Store) notify.Loader {
	return func(ctx context.Context) ([]notify.Config, error) {
		userID, err := st.EnsureDefaultUser(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := st.ListNotificationChannels(ctx, userID)
		if err != nil {
			return nil, err
		}
		out := make([]notify.Config, 0, len(rows))
		for _, r := range rows {
			events := map[notify.Event]bool{}
			if r.Events != "" && r.Events != "{}" {
				_ = json.Unmarshal([]byte(r.Events), &events)
			}
			out = append(out, notify.Config{
				Type:    r.Type,
				Config:  json.RawMessage(r.Config),
				Events:  events,
				Enabled: r.Enabled,
			})
		}
		return out, nil
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
