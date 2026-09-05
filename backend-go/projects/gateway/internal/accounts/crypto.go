// Package accounts owns the M08 vertical slice: the /accounts (admin) +
// /my-accounts (self) route family ported from
// backend/src/modules/accounts/accounts.routes.ts plus the account.*
// repositories under backend/src/storage/. The slice covers the paged
// management list, the options dropdown, the owner-scoped edit-basic detail,
// guarded create with AES-GCM sealed credentials, the basic-config patch with
// config_revision optimistic locking, the lock/unlock/lock-config family, the
// soft delete with related cleanup and the account tag endpoints. The M09
// companion files add the batch-edit context/update, the CCS import
// preview/confirm pipeline and the native export document. The clone
// context, upstream model catalog sync, credential normalization services and
// the balance health probes remain companion slices.
package accounts

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
	"strconv"
	"strings"
	"time"
)

// EncryptJSON mirrors storage/crypto.ts encryptJson: AES-256-GCM with a
// sha256(secret) key and a random 12-byte IV, sealed as
// "v1:{iv}:{tag}:{ciphertext}" with raw (unpadded) base64url parts. Existing
// Node accounts.credentials_encrypted rows decrypt byte-for-byte through
// DecryptJSON.
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

// HashSecret mirrors hashSecret: sha256 hex digest (credential fingerprint
// material, shared with the api_keys slice).
func HashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// MaskSecret mirrors maskSecret: short secrets keep head+tail pairs, longer
// ones keep a 6/4 split. The masked column and every log/response surface use
// this shape so credential material never appears in clear text.
func MaskSecret(value any) string {
	text, ok := value.(string)
	if !ok || len(text) == 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) == 1 {
		// Node slice(-2) is safe on a single rune; Go slicing would panic.
		return string(runes[:1]) + "***"
	}
	if len(runes) <= 10 {
		return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
	}
	return string(runes[:6]) + "***" + string(runes[len(runes)-4:])
}

// NewAccountID mirrors Node newId('acc'):
// "acc_{Date.now()}_{8 hex chars}".
func NewAccountID() string {
	return newID("acc")
}

// NewTagID mirrors Node newId('acctag').
func NewTagID() string { return newID("acctag") }

func newID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + "_" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "_" +
		hex.EncodeToString(buf)[:8]
}
