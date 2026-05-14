package zlibrarymcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

const (
	privateKeyFile = "private_key.pem"
	publicKeyFile  = "public_key.pem"
)

type rsaKeyPair struct {
	PrivatePath string
	PublicPath  string
	PublicPEM   string
}

func ensureRSAKeyPair(root string) (*rsaKeyPair, error) {
	keyDir := filepath.Join(root, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return nil, err
	}
	privatePath := filepath.Join(keyDir, privateKeyFile)
	publicPath := filepath.Join(keyDir, publicKeyFile)

	if privateBytes, err := os.ReadFile(privatePath); err == nil {
		privateKey, err := parseRSAPrivateKey(privateBytes)
		if err != nil {
			return nil, fmt.Errorf("parse existing private key: %w", err)
		}
		_ = os.Chmod(privatePath, 0o600)
		publicPEM, err := ensurePublicKeyFile(publicPath, &privateKey.PublicKey)
		if err != nil {
			return nil, err
		}
		return &rsaKeyPair{PrivatePath: privatePath, PublicPath: publicPath, PublicPEM: publicPEM}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, err
	}
	if err := privateKey.Validate(); err != nil {
		return nil, err
	}

	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	if privatePEM == nil {
		return nil, fmt.Errorf("encode private key")
	}
	if err := os.WriteFile(privatePath, privatePEM, 0o600); err != nil {
		return nil, err
	}

	publicPEM, err := writePublicKey(publicPath, &privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	return &rsaKeyPair{PrivatePath: privatePath, PublicPath: publicPath, PublicPEM: publicPEM}, nil
}

func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("missing PEM block")
	}
	if block.Type == "RSA PRIVATE KEY" {
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, not RSA", key)
	}
	return rsaKey, nil
}

func ensurePublicKeyFile(path string, publicKey *rsa.PublicKey) (string, error) {
	if existing, err := os.ReadFile(path); err == nil {
		if _, err := parseRSAPublicKey(existing); err == nil {
			_ = os.Chmod(path, 0o644)
			return string(existing), nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return writePublicKey(path, publicKey)
}

func writePublicKey(path string, publicKey *rsa.PublicKey) (string, error) {
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", err
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	if publicPEM == nil {
		return "", fmt.Errorf("encode public key")
	}
	if err := os.WriteFile(path, publicPEM, 0o644); err != nil {
		return "", err
	}
	return string(publicPEM), nil
}

func parseRSAPublicKey(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("missing PEM block")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, not RSA", key)
	}
	return rsaKey, nil
}
