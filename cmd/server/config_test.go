package main

import "testing"

func TestNormalizePublicBase(t *testing.T) {
	cases := map[string]string{
		"":                            "http://localhost:8080",
		"http://localhost:8080":       "http://localhost:8080",
		"https://fynanz.example.com":  "https://fynanz.example.com",
		"fynanz.example.com":          "https://fynanz.example.com",
		"fynanz.example.com/":         "https://fynanz.example.com",
		"FYNANZ.EXAMPLE.COM":          "https://FYNANZ.EXAMPLE.COM",
		"localhost:8080":              "http://localhost:8080",
		"127.0.0.1:8080":              "http://127.0.0.1:8080",
		"  fynanz.example.com:8443  ": "https://fynanz.example.com:8443",
	}
	for in, want := range cases {
		if got := normalizePublicBase(in); got != want {
			t.Errorf("normalizePublicBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultSyncHistoryDays(t *testing.T) {
	// The dashboard's 12M/1y views need a year of data to show anything in
	// the early slots — keep the sync window at least a year by default.
	if d := getenvInt("SYNC_HISTORY_DAYS", 365); d != 365 {
		t.Errorf("default SYNC_HISTORY_DAYS = %d, want 365", d)
	}
	if got := getenvInt("SYNC_HISTORY_DAYS_UNSET", 365); got < 365 {
		t.Errorf("fallback SYNC_HISTORY_DAYS = %d, want >= 365", got)
	}
}
