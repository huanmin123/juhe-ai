package accountbalance

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// EncryptV1Envelope and DecryptV1Envelope intentionally use the same stable
// v1 wire layout as the existing jobs credential boundary.  They are kept
// local so J2 does not import or call the J1 execution package.
func EncryptV1Envelope(secret string, plaintext []byte) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("凭据 secret 不能为空")
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
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成凭据 nonce 失败: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	if len(sealed) < gcm.Overhead() {
		return "", errors.New("凭据 GCM 输出无效")
	}
	cut := len(sealed) - gcm.Overhead()
	return "v1:" + base64.RawURLEncoding.EncodeToString(nonce) + ":" + base64.RawURLEncoding.EncodeToString(sealed[cut:]) + ":" + base64.RawURLEncoding.EncodeToString(sealed[:cut]), nil
}

func DecryptV1Envelope(secret, envelope string) ([]byte, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("凭据 secret 不能为空")
	}
	parts := strings.Split(strings.TrimSpace(envelope), ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return nil, errors.New("不支持的凭据 envelope 格式")
	}
	decode := func(value string) ([]byte, error) {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return nil, errors.New("解码凭据 envelope 失败")
		}
		return decoded, nil
	}
	nonce, err := decode(parts[1])
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
	if len(nonce) != 12 || len(tag) != 16 {
		return nil, errors.New("凭据 envelope 的 nonce 或 tag 长度无效")
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
	plain, err := gcm.Open(nil, nonce, append(ciphertext, tag...), nil)
	if err != nil {
		return nil, errors.New("凭据 envelope 认证失败")
	}
	return plain, nil
}

func NewCredentialEnvelope(secret, kind string, value any) (CredentialEnvelope, error) {
	if strings.TrimSpace(kind) == "" {
		return CredentialEnvelope{}, errors.New("凭据 kind 不能为空")
	}
	plaintext, err := json.Marshal(value)
	if err != nil {
		return CredentialEnvelope{}, fmt.Errorf("编码凭据失败: %w", err)
	}
	ciphertext, err := EncryptV1Envelope(secret, plaintext)
	if err != nil {
		return CredentialEnvelope{}, err
	}
	return CredentialEnvelope{Kind: kind, Ciphertext: ciphertext}, nil
}

func openCredential(secret string, envelope CredentialEnvelope, expectedKind string, target any) error {
	if strings.TrimSpace(envelope.Kind) == "" || strings.TrimSpace(envelope.Ciphertext) == "" {
		return errors.New("凭据 envelope 不完整")
	}
	if expectedKind != "" && envelope.Kind != expectedKind {
		return fmt.Errorf("凭据 envelope kind 不匹配")
	}
	plain, err := DecryptV1Envelope(secret, envelope.Ciphertext)
	if err != nil {
		return err
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(plain, target); err != nil {
		return errors.New("解析凭据 envelope 失败")
	}
	return nil
}
