package oauthmgmt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// encryptJSON mirrors storage/crypto.ts encryptJson (same contract as the
// apikeys/accounts slices): AES-256-GCM with a sha256(secret) key and a random
// 12-byte IV, sealed as "v1:{iv}:{tag}:{ciphertext}" with raw (unpadded)
// base64url parts. The functions stay package-private: M17 copies the crypto
// boundary instead of widening the apikeys/accounts export surface, and rows
// written here decrypt byte-for-byte through the Node decryptJson.
func encryptJSON(secret string, value any) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(secret)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, plain, nil)
	ciphertext, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	encode := base64.RawURLEncoding.EncodeToString
	return strings.Join([]string{
		"v1",
		encode(iv),
		encode(tag),
		encode(ciphertext),
	}, ":"), nil
}

// decryptJSON mirrors storage/crypto.ts decryptJson: only the v1 envelope is
// accepted and the GCM tag is verified before JSON decoding.
func decryptJSON(secret string, envelope string, target any) error {
	parts := strings.Split(strings.TrimSpace(envelope), ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return errors.New("加密数据格式不受支持")
	}
	decode := func(value string) ([]byte, error) {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return nil, errors.New("加密数据格式不受支持")
		}
		return raw, nil
	}
	iv, err := decode(parts[1])
	if err != nil {
		return err
	}
	tag, err := decode(parts[2])
	if err != nil {
		return err
	}
	ciphertext, err := decode(parts[3])
	if err != nil {
		return err
	}
	gcm, err := newGCM(secret)
	if err != nil {
		return err
	}
	if len(iv) != gcm.NonceSize() || len(tag) != gcm.Overhead() {
		return errors.New("加密数据格式不受支持")
	}
	plain, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return errors.New("加密数据格式不受支持")
	}
	return json.Unmarshal(plain, target)
}

// newGCM derives the AES-256-GCM block cipher exactly like Node:
// createHash('sha256').update(secret).digest().
func newGCM(secret string) (cipher.AEAD, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm unavailable: %w", err)
	}
	return gcm, nil
}

// hashSecret mirrors hashSecret / accountCredentialFingerprint material:
// sha256 hex digest of the trimmed source secret (accounts.credential_fingerprint).
func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// maskSecret mirrors maskSecret: short secrets keep head+tail pairs, longer
// ones keep a 6/4 split (accounts.credential_mask).
func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 10 {
		return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
	}
	return string(runes[:6]) + "***" + string(runes[len(runes)-4:])
}
