package accounthealth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const signedInputAlgorithm = "hmac-sha256-v1"

// SignedInputEnvelope carries the exact UTF-8 input JSON as base64url so both
// sides verify the same bytes. This deliberately avoids a hidden
// cross-language JSON canonicalization dependency.
type SignedInputEnvelope struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func VerifySignedInput(raw []byte, keys map[string][]byte) (Input, error) {
	payload, err := VerifySignedPayload(raw, keys)
	if err != nil {
		return Input{}, err
	}
	var input Input
	if err := json.Unmarshal(payload, &input); err != nil {
		return Input{}, fmt.Errorf("解析 input payload 失败: %w", err)
	}
	return input, nil
}

// VerifySignedPayload validates the exact Node-published envelope and returns
// its original payload bytes. Input and request files share this wire format
// but intentionally decode into different typed contracts.
func VerifySignedPayload(raw []byte, keys map[string][]byte) ([]byte, error) {
	var envelope SignedInputEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("解析签名 envelope 失败: %w", err)
	}
	if envelope.Algorithm != signedInputAlgorithm || strings.TrimSpace(envelope.KeyID) == "" {
		return nil, errors.New("不支持的 input 签名算法或 key ID")
	}
	key, found := keys[envelope.KeyID]
	if !found || len(key) == 0 {
		return nil, errors.New("input 签名 key 不可用")
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return nil, errors.New("input payload 编码无效")
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return nil, errors.New("input signature 编码无效")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(signedInputAlgorithm + "\n" + envelope.KeyID + "\n"))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, errors.New("input 签名校验失败")
	}
	return payload, nil
}
