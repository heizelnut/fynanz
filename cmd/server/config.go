package main

import (
	"os"
	"strconv"
	"time"
)

// Config is read from the environment (see .env.example). v1 has no auth; the
// network boundary is the trust boundary (AGENTS.md §2).
type Config struct {
	Port           string
	DBPath         string
	EBBaseURL      string
	EBAppID        string
	EBCertPath     string // RSA private key .pem registered with Enable Banking
	EBRedirectURL  string
	PSUType        string
	SyncInterval   time.Duration
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
		Port:           getenv("PORT", "8080"),
		DBPath:         getenv("DB_PATH", "./data/fynanz.db"),
		EBBaseURL:      getenv("ENABLE_BANKING_URL", sandboxDefaultURL),
		EBAppID:        os.Getenv("ENABLE_BANKING_APP_ID"),
		EBCertPath:     os.Getenv("ENABLE_BANKING_PRIVATE_KEY"),
		EBRedirectURL:  getenv("ENABLE_BANKING_REDIRECT_URL", "http://localhost:8080/links/callback"),
		PSUType:        getenv("ENABLE_BANKING_PSU_TYPE", "personal"),
		SyncInterval:   getenvDuration("SYNC_INTERVAL", 24*time.Hour),
		HistoryDays:    getenvInt("SYNC_HISTORY_DAYS", 90),
		MaxConsentDays: getenvInt("ENABLE_BANKING_MAX_CONSENT_DAYS", 30),
		PublicBaseURL:  getenv("PUBLIC_BASE_URL", "http://localhost:8080"),
	}
}
