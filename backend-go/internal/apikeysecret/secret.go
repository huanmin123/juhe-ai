package apikeysecret

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

func Generate() (string, error) {
	var bytes [32]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(bytes[:]), nil
}

func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func Prefix(secret string) string {
	if len(secret) <= 8 {
		return secret
	}
	return secret[:8]
}

func Suffix(secret string) string {
	if len(secret) <= 8 {
		return secret
	}
	return secret[len(secret)-8:]
}
