package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"testing"

	"fynanz/internal/store"
)

func pemOf(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestResolveEBPrecedence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "eb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	uid, _ := st.EnsureDefaultUser(ctx)

	cfg := Config{
		EBAppID: "env-app", EBCertPath: "", EBBaseURL: "https://env-url",
		PSUType: "personal", MaxConsentDays: 30, PublicBaseURL: "https://fynanz.example.com",
	}

	// No DB entries → env defaults, no key.
	state, key, err := resolveEB(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if state.AppID != "env-app" || state.BaseURL != "https://env-url" || state.KeySet || key != nil {
		t.Errorf("env fallback wrong: %+v", state)
	}
	if state.RedirectURL != "https://fynanz.example.com/links/callback" {
		t.Errorf("redirect wrong: %s", state.RedirectURL)
	}

	// DB overrides win; key stored in DB is picked up.
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	if err := st.SetSetting(ctx, uid, store.SettingEBAppID, "db-app"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, uid, store.SettingEBPrivateKey, pemOf(t, priv)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, uid, store.SettingEBMaxConsentDays, "7"); err != nil {
		t.Fatal(err)
	}
	state, key, err = resolveEB(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if state.AppID != "db-app" || !state.KeySet || state.KeySource != "db" || state.MaxConsentDays != "7" {
		t.Errorf("db override wrong: %+v", state)
	}
	if key == nil {
		t.Fatal("stored key not parsed")
	}
	if key.N.Cmp(priv.N) != 0 {
		t.Errorf("wrong key parsed")
	}

	// Clearing app id falls back to env, key still from DB.
	if err := st.SetSetting(ctx, uid, store.SettingEBAppID, ""); err != nil {
		t.Fatal(err)
	}
	state, _, err = resolveEB(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if state.AppID != "env-app" {
		t.Errorf("after clear want env fallback, got %q", state.AppID)
	}
	if !state.KeySet {
		t.Error("key should still be set from DB")
	}
}
