package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	federationPrivateKeyFile        = "federation-ed25519.pk8.pem"
	federationPendingPrivateKeyFile = "federation-ed25519.pending.pk8.pem"
)

var errFederationRotationPending = errors.New("federation key rotation is already pending")

func federationPendingKeyPath(directory string) string {
	return filepath.Join(directory, federationPendingPrivateKeyFile)
}

type federationSigningKey struct {
	ID         string
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

func loadOrCreateFederationSigningKey(directory string) (federationSigningKey, error) {
	if directory == "" {
		return federationSigningKey{}, errors.New("federation key directory is required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return federationSigningKey{}, fmt.Errorf("create federation key directory: %w", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return federationSigningKey{}, fmt.Errorf("protect federation key directory: %w", err)
	}
	path := filepath.Join(directory, federationPrivateKeyFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := writeNewFederationSigningKey(path); err != nil {
			return federationSigningKey{}, err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return federationSigningKey{}, fmt.Errorf("inspect federation private key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return federationSigningKey{}, errors.New("federation private key must be a regular file with owner-only permissions")
	}
	return readFederationSigningKey(path)
}

func readFederationSigningKey(path string) (federationSigningKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return federationSigningKey{}, fmt.Errorf("inspect federation private key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return federationSigningKey{}, errors.New("federation private key must be a regular file with owner-only permissions")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return federationSigningKey{}, fmt.Errorf("read federation private key: %w", err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		return federationSigningKey{}, errors.New("invalid federation private key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return federationSigningKey{}, errors.New("invalid federation PKCS#8 key")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return federationSigningKey{}, errors.New("federation key must be Ed25519")
	}
	return federationKeyFromPrivate(privateKey), nil
}

func writeNewFederationSigningKey(path string) (federationSigningKey, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return federationSigningKey{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return federationSigningKey{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return federationSigningKey{}, fmt.Errorf("create federation private key: %w", err)
	}
	if _, err := file.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		file.Close()
		os.Remove(path)
		return federationSigningKey{}, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return federationSigningKey{}, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return federationSigningKey{}, err
	}
	if err := syncFederationKeyDirectory(filepath.Dir(path)); err != nil {
		os.Remove(path)
		return federationSigningKey{}, err
	}
	return federationKeyFromPrivate(privateKey), nil
}

func federationKeyFromPrivate(privateKey ed25519.PrivateKey) federationSigningKey {
	publicKey := privateKey.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(publicKey)
	return federationSigningKey{ID: "cookbook-ed25519-" + hex.EncodeToString(digest[:16]), PrivateKey: privateKey, PublicKey: publicKey}
}

func stageFederationKeyRotation(directory string) (federationSigningKey, error) {
	if _, err := loadOrCreateFederationSigningKey(directory); err != nil {
		return federationSigningKey{}, err
	}
	pendingPath := federationPendingKeyPath(directory)
	if _, err := os.Lstat(pendingPath); err == nil {
		return federationSigningKey{}, errFederationRotationPending
	} else if !errors.Is(err, os.ErrNotExist) {
		return federationSigningKey{}, err
	}
	key, err := writeNewFederationSigningKey(pendingPath)
	if errors.Is(err, os.ErrExist) {
		return federationSigningKey{}, errFederationRotationPending
	}
	return key, err
}

func activateFederationKeyRotation(directory string) (federationSigningKey, error) {
	if _, err := loadOrCreateFederationSigningKey(directory); err != nil {
		return federationSigningKey{}, err
	}
	pendingPath := federationPendingKeyPath(directory)
	key, err := readFederationSigningKey(pendingPath)
	if err != nil {
		return federationSigningKey{}, err
	}
	if err := os.Rename(pendingPath, filepath.Join(directory, federationPrivateKeyFile)); err != nil {
		return federationSigningKey{}, fmt.Errorf("activate federation signing key: %w", err)
	}
	if err := syncFederationKeyDirectory(directory); err != nil {
		return federationSigningKey{}, err
	}
	return key, nil
}

func syncFederationKeyDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open federation key directory: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync federation key directory: %w", err)
	}
	return nil
}

func (key federationSigningKey) publicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(key.PublicKey)
}
