package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const defaultSecret = "juhe-ai-go-development-secret"

type JSONCodec struct {
	key [32]byte
}

func NewJSONCodec(secret string) JSONCodec {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = defaultSecret
	}
	return JSONCodec{key: sha256.Sum256([]byte(secret))}
}

func (c JSONCodec) EncryptJSON(value map[string]any) (string, error) {
	aead, err := c.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plain, nil)
	ciphertext := sealed[:len(sealed)-aead.Overhead()]
	tag := sealed[len(sealed)-aead.Overhead():]
	encode := base64.RawURLEncoding.EncodeToString
	return "v1:" + encode(nonce) + ":" + encode(tag) + ":" + encode(ciphertext), nil
}

func (c JSONCodec) DecryptJSON(value string) (map[string]any, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return nil, fmt.Errorf("unsupported encrypted credential format")
	}
	decode := base64.RawURLEncoding.DecodeString
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
	aead, err := c.aead()
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() || len(tag) != aead.Overhead() {
		return nil, fmt.Errorf("unsupported encrypted credential format")
	}
	sealed := append(append(make([]byte, 0, len(ciphertext)+len(tag)), ciphertext...), tag...)
	plain, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c JSONCodec) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
