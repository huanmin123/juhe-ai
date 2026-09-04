// Package apikeys owns the M07 vertical slice: the /api-keys (admin) +
// /my-api-keys (self) route family ported from
// backend/src/modules/api-keys/api-keys.routes.ts plus the api-key.*
// repositories under backend/src/storage/. The slice covers the paged list
// (masked keys only), the owner-scoped detail, the one-shot secret reveal,
// guarded create with AES-GCM sealed plaintext, secret refresh with triple
// cache invalidation and the atomic hard delete that enqueues a
// api_key_record_cleanup_targets row. PATCH /:id (revision-locked update),
// the request-quota hourly window worker and the J5 usage summaries are
// companion slices; the usage projections here render the shared zero value.
package apikeys

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

// EncryptJSON mirrors storage/crypto.ts encryptJson: AES-256-GCM with a
// sha256(secret) key and a random 12-byte IV, sealed as
// "v1:{iv}:{tag}:{ciphertext}" with raw (unpadded) base64url parts. Existing
// Node rows decrypt byte-for-byte through DecryptJSON.
func EncryptJSON(secret string, value any) (string, error) {
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

// DecryptJSON mirrors storage/crypto.ts decryptJson: only the v1 envelope is
// accepted and the GCM tag is verified before JSON decoding.
func DecryptJSON(secret string, envelope string, target any) error {
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

// HashSecret mirrors hashSecret: sha256 hex digest of the plaintext key
// (api_keys.key_hash lookup material).
func HashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// NewAPIKey mirrors createApiKey: "sk-" + 32 random bytes hex (67 chars).
func NewAPIKey() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return "sk-" + hex.EncodeToString(buf)
}

// secretPayload mirrors the encryptJson({ key }) envelope Node stores in
// api_keys.key_secret_encrypted.
type secretPayload struct {
	Key string `json:"key"`
}
