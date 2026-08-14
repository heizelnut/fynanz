package sync

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// ParseRSAPrivateKey accepts PKCS#1 ("BEGIN RSA PRIVATE KEY") and PKCS#8
// ("BEGIN PRIVATE KEY") PEM — the browser-generated key at Enable Banking
// registration is PKCS#8, openssl genrsa produces PKCS#1.
func ParseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block in private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := k.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("PKCS#8 key is not RSA")
	}
	return nil, errors.New("unsupported private key format (want PKCS#1 or PKCS#8 RSA PEM)")
}
