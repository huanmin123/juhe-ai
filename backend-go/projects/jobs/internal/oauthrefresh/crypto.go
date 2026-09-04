package oauthrefresh

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

// The AES v1 credential envelope mirrors backend/src/storage/crypto.ts:
// AES-256-GCM keyed by sha256(secret), random 12-byte IV, sealed as
// "v1:{iv}:{tag}:{ciphertext}" with raw (unpadded) base64url parts. Accounts
// rows sealed by Node decrypt here and rows re-sealed here decrypt through the
// Node decryptJson, so legacy credentials stay refreshable after the cutover.

// EncryptJSON seals a JSON-serializable value with the runtime secret.
func EncryptJSON(secret string, value any) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("生成凭据 IV 失败: %w", err)
	}
	return sealJSONWithIV(secret, plain, iv)
}

// DecryptJSON opens a v1 envelope into target. Only the v1 envelope is
// accepted; GCM authentication failures surface as the Node error copy.
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
	gcm, err := newEnvelopeGCM(secret)
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

// sealJSONWithIV is the deterministic write side used by EncryptJSON and the
// cross-language vector tests (the IV is normally random).
func sealJSONWithIV(secret string, plain, iv []byte) (string, error) {
	gcm, err := newEnvelopeGCM(secret)
	if err != nil {
		return "", err
	}
	if len(iv) != gcm.NonceSize() {
		return "", errors.New("加密数据 IV 长度无效")
	}
	sealed := gcm.Seal(nil, iv, plain, nil)
	ciphertext, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	encode := base64.RawURLEncoding.EncodeToString
	return strings.Join([]string{"v1", encode(iv), encode(tag), encode(ciphertext)}, ":"), nil
}

// newEnvelopeGCM derives the AES-256-GCM block cipher exactly like Node:
// createHash('sha256').update(secret).digest().
func newEnvelopeGCM(secret string) (cipher.AEAD, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("aes-256-gcm unavailable: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-256-gcm unavailable: %w", err)
	}
	return gcm, nil
}

// hashSecret mirrors storage/crypto.ts hashSecret (sha256 hex of the trimmed
// credential source; accounts.credential_fingerprint material).
func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// maskSecret mirrors storage/crypto.ts maskSecret: short secrets keep head+tail
// pairs, longer ones a 6/4 split (accounts.credential_mask material).
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
