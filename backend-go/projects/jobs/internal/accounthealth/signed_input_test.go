package accounthealth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestVerifySignedInputAcceptsExactPayload(t *testing.T) {
	input := testInput("https://api.example.com", "chat_json")
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	raw := signedEnvelope(t, "current", []byte("signing-key"), payload)
	actual, err := VerifySignedInput(raw, map[string][]byte{"current": []byte("signing-key")})
	if err != nil || actual.AccountID != input.AccountID || actual.InputVersion != input.InputVersion {
		t.Fatalf("verified input=%#v err=%v", actual, err)
	}
}

func TestVerifySignedInputRejectsTampering(t *testing.T) {
	payload := []byte(`{"account_id":"account-1"}`)
	raw := signedEnvelope(t, "current", []byte("signing-key"), payload)
	var envelope SignedInputEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Payload = base64.RawURLEncoding.EncodeToString([]byte(`{"account_id":"account-2"}`))
	updated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySignedInput(updated, map[string][]byte{"current": []byte("signing-key")}); err == nil {
		t.Fatal("tampered input must be rejected")
	}
}

func signedEnvelope(t *testing.T, keyID string, key, payload []byte) []byte {
	t.Helper()
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(signedInputAlgorithm + "\n" + keyID + "\n"))
	_, _ = mac.Write(payload)
	raw, err := json.Marshal(SignedInputEnvelope{
		Algorithm: signedInputAlgorithm,
		KeyID:     keyID,
		Payload:   base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
