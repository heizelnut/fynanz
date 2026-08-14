package sync

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignClientJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	token, err := signClientJWT("app-123", key, now)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	decode := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("b64 decode: %v", err)
		}
		return b
	}

	var header struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Typ != "JWT" || header.Alg != "RS256" || header.Kid != "app-123" {
		t.Errorf("bad header: %+v", header)
	}

	var claims struct {
		Iss string `json:"iss"`
		Aud string `json:"aud"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(decode(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "enablebanking.com" || claims.Aud != "api.enablebanking.com" {
		t.Errorf("bad claims: %+v", claims)
	}
	if claims.Exp-claims.Iat != int64(jwtTTL.Seconds()) {
		t.Errorf("ttl = %ds, want %ds", claims.Exp-claims.Iat, int64(jwtTTL.Seconds()))
	}

	// Verify the signature with the public key.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}

	// And ensure the key round-trips through the parser used by cmd/server
	// (PKCS#1 and PKCS#8 PEM forms).
	for _, der := range [][]byte{
		func() []byte {
			b, _ := x509.MarshalPKCS8PrivateKey(key)
			return b
		}(),
		x509.MarshalPKCS1PrivateKey(key),
	} {
		if _, err := x509.ParsePKCS1PrivateKey(der); err != nil {
			if k, err2 := x509.ParsePKCS8PrivateKey(der); err2 != nil || k == nil {
				t.Errorf("key not parseable as PKCS#1 or PKCS#8: %v / %v", err, err2)
			}
		}
	}
}
