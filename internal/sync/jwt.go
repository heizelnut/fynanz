package sync

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// Enable Banking authenticates every API request with a self-signed JWT
// (RS256), never exchanged for an access token:
//
//	header: {typ: "JWT", alg: "RS256", kid: <application_id>}
//	claims: {iss: "enablebanking.com", aud: "api.enablebanking.com", iat, exp}
//
// TTL is capped at 24h by the API; we use 1h per request so no clock skew
// concerns accumulate. See securityScheme docs in the OpenAPI spec.

const (
	jwtIssuer   = "enablebanking.com"
	jwtAudience = "api.enablebanking.com"
	jwtTTL      = time.Hour
)

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// signClientJWT builds the authorization JWT for one API call.
func signClientJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := map[string]string{"typ": "JWT", "alg": "RS256", "kid": appID}
	claims := map[string]any{
		"iss": jwtIssuer,
		"aud": jwtAudience,
		"iat": now.Unix(),
		"exp": now.Add(jwtTTL).Unix(),
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64url(h) + "." + b64url(c)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("jwt: sign: %w", err)
	}
	return signingInput + "." + b64url(sig), nil
}
