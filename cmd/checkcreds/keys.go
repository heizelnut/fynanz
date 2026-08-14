package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
)

// loadKey accepts both PKCS#1 ("BEGIN RSA PRIVATE KEY") and PKCS#8
// ("BEGIN PRIVATE KEY") PEM — the browser-generated key at registration is
// PKCS#8, openssl genrsa produces PKCS#1.
func loadKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := k.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("key is not RSA")
	}
	return nil, errors.New("unsupported PEM (want PKCS#1 or PKCS#8 RSA private key)")
}
