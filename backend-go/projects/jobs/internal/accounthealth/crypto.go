package accounthealth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// EncryptV1Envelope is the write-side of the stable credential data format.
// Direct-input assembly uses it only in memory for the jobs-owned Input
// boundary (for example a resolved proxy URL); it must never persist plaintext
// credentials or modify the Node business database.
func EncryptV1Envelope(secret string, plaintext []byte) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("凭据 secret 不能为空")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", fmt.Errorf("创建凭据加密器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建凭据 GCM 加密器失败: %w", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("生成凭据 IV 失败: %w", err)
	}
	ciphertextAndTag := gcm.Seal(nil, iv, plaintext, nil)
	if len(ciphertextAndTag) < 16 {
		return "", fmt.Errorf("凭据 GCM 输出无效")
	}
	cut := len(ciphertextAndTag) - 16
	return "v1:" + base64.RawURLEncoding.EncodeToString(iv) + ":" + base64.RawURLEncoding.EncodeToString(ciphertextAndTag[cut:]) + ":" + base64.RawURLEncoding.EncodeToString(ciphertextAndTag[:cut]), nil
}

// DecryptV1Envelope implements the existing stable v1 credential envelope.
// It is a data-format compatibility boundary, not a service call.
func DecryptV1Envelope(secret, envelope string) ([]byte, error) {
	parts := strings.Split(strings.TrimSpace(envelope), ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return nil, fmt.Errorf("不支持的凭据 envelope 格式")
	}
	decode := func(value string) ([]byte, error) {
		result, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("解码凭据 envelope 失败: %w", err)
		}
		return result, nil
	}
	iv, err := decode(parts[1])
	if err != nil {
		return nil, err
	}
	tag, err := decode(parts[2])
	if err != nil {
		return nil, err
	}
	ciphertext, err := decode(parts[3])
	if err != nil {
		return nil, err
	}
	if len(iv) != 12 || len(tag) != 16 {
		return nil, fmt.Errorf("凭据 envelope 的 IV 或 tag 长度无效")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("创建凭据解密器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建凭据 GCM 解密器失败: %w", err)
	}
	plaintext, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return nil, fmt.Errorf("凭据 envelope 认证失败")
	}
	return plaintext, nil
}
