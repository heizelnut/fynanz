package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read from the environment (see .env.example). v1 has no auth; the
// network boundary is the trust boundary (AGENTS.md §2).
type Config struct {
	Port          string
	DBPath        string
	EBBaseURL     string
	EBAppID       string
	EBCertPath    string // RSA private key .pem registered with Enable Banking
	EBRedirectURL string
	PSUType       string
	SyncInterval  time.Duration
	// Sync history window: how far back EVERY sync fetches transactions
	// (time.Now - SYNC_HISTORY_DAYS). This is a rolling window, not a one-time
	// backfill: a re-sync with a larger value pulls deeper history and merges it
	// without duplicates (transactions dedupe on entry_reference), up to what the
	// bank/ASPSP serves under PSD2 (many cap at ~90 days).
	HistoryDays    int
	MaxConsentDays int
	PublicBaseURL  string // outside origin, used for notification deep links
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func loadConfig() Config {
	return Config{
		Port:          getenv("PORT", "8080"),
		DBPath:        getenv("DB_PATH", "./data/fynanz.db"),
		EBBaseURL:     getenv("ENABLE_BANKING_URL", sandboxDefaultURL),
		EBAppID:       os.Getenv("ENABLE_BANKING_APP_ID"),
		EBCertPath:    os.Getenv("ENABLE_BANKING_PRIVATE_KEY"),
		EBRedirectURL: getenv("ENABLE_BANKING_REDIRECT_URL", "http://localhost:8080/links/callback"),
		PSUType:       getenv("ENABLE_BANKING_PSU_TYPE", "personal"),
		SyncInterval:  getenvDuration("SYNC_INTERVAL", 24*time.Hour),
		// Sync history window: how far back EVERY sync fetches transactions
		// (time.Now - SYNC_HISTORY_DAYS). This is a rolling window, not a one-time
		// backfill: a re-sync with a larger value pulls deeper history and merges it
		// without duplicates (transactions dedupe on entry_reference), up to what the
		// bank/ASPSP serves under PSD2 (many cap at ~90 days).
		HistoryDays:    getenvInt("SYNC_HISTORY_DAYS", 365),
		MaxConsentDays: getenvInt("ENABLE_BANKING_MAX_CONSENT_DAYS", 30),
		PublicBaseURL:  normalizePublicBase(getenv("PUBLIC_BASE_URL", "http://localhost:8080")),
	}
}

// normalizePublicBase guarantees the public origin has a scheme: ntfy
// click/actions and the consent redirect need absolute URLs, and a user may
// set PUBLIC_BASE_URL without one (e.g. "fynanz.example.com"). https:// is
// assumed; http:// only for localhost-style hosts.
func normalizePublicBase(raw string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	if raw == "" {
		return "http://localhost:8080"
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	host := raw
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") || strings.HasPrefix(host, "::1") {
		return "http://" + raw
	}
	return "https://" + raw
}
