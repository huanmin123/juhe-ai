package accountcrypto

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Golden vectors sealed by Node crypto.ts (encryptJson) with the same secret
// and IV, locking the AES v1 envelope byte-for-byte across runtimes:
//
//	key   = sha256('test-secret')
//	iv    = 000102030405060708090a0b (vector1) / 0b0a09080706050403020100 (vector2)
const (
	cryptoTestSecret = "test-secret"
	// vector1Plaintext == {"access_token":"at-1","refresh_token":"rt-1"}
	vector1Envelope = "v1:AAECAwQFBgcICQoL:zWv4xl18RW7m8C0BPN8Wkw:PLur9vS-VeeayjC7eOdBWV-yXzQNY2pRXJbSO0pqfEHhBp-qolESZKwaVWIUBQ"
	// vector2 exercises unicode keys/values and nested objects.
	vector2Envelope = "v1:CwoJCAcGBQQDAgEA:9HJBuC-9cxSitL_JefC6BA:WqJxMfoxN3TZpx1gWD2VZsQLOVbDDqOcZ4sA7S_LFU_g6JXUUHrB3CwijuVO"
)

func mustDecodeBase64URL(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func tamperEnvelope(t *testing.T, envelope string) string {
	t.Helper()
	index := strings.LastIndex(envelope, ":")
	if index < 0 {
		t.Fatal("envelope missing ciphertext segment")
	}
	ciphertext := []byte(envelope[index+1:])
	if ciphertext[0] == 'A' {
		ciphertext[0] = 'B'
	} else {
		ciphertext[0] = 'A'
	}
	return envelope[:index+1] + string(ciphertext)
}

// TestEncryptJSONWithFixedIVMatchesNodeEnvelope locks the seal side against
// the Node golden envelope (原 oauthrefresh crypto_test 同款向量).
func TestEncryptJSONWithFixedIVMatchesNodeEnvelope(t *testing.T) {
	plain := []byte(`{"access_token":"at-1","refresh_token":"rt-1"}`)
	iv := mustDecodeBase64URL(t, "AAECAwQFBgcICQoL")
	sealed, err := sealJSONWithIV(cryptoTestSecret, plain, iv)
	if err != nil {
		t.Fatal(err)
	}
	if sealed != vector1Envelope {
		t.Fatalf("Go seal != Node envelope:\n got %s\nwant %s", sealed, vector1Envelope)
	}
}

func TestDecryptJSONReadsNodeEnvelope(t *testing.T) {
	var credentials map[string]any
	if err := DecryptJSON(cryptoTestSecret, vector1Envelope, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials["access_token"] != "at-1" || credentials["refresh_token"] != "rt-1" {
		t.Fatalf("vector1 decrypted=%v", credentials)
	}
	// vector2: unicode + nested object sealed by Node.
	var nested map[string]any
	if err := DecryptJSON(cryptoTestSecret, vector2Envelope, &nested); err != nil {
		t.Fatal(err)
	}
	if nested["中文"] != "值" {
		t.Fatalf("vector2 中文 key=%v", nested["中文"])
	}
	object, ok := nested["nested"].(map[string]any)
	if !ok || object["b"] != float64(1) {
		t.Fatalf("vector2 nested=%v", nested["nested"])
	}
}

func TestDecryptJSONRejectsWrongSecret(t *testing.T) {
	var credentials map[string]any
	if err := DecryptJSON("other-secret", vector1Envelope, &credentials); err == nil {
		t.Fatal("wrong secret must fail GCM authentication")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	value := map[string]any{
		"access_token":  "at-new",
		"refresh_token": "rt-new",
		"expires_at":    "2026-09-04T10:00:00.000Z",
		"plan_type":     "pro",
	}
	sealed, err := EncryptJSON(cryptoTestSecret, value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("envelope prefix=%s", sealed)
	}
	var decoded map[string]any
	if err := DecryptJSON(cryptoTestSecret, sealed, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["access_token"] != "at-new" || decoded["expires_at"] != "2026-09-04T10:00:00.000Z" {
		t.Fatalf("roundtrip=%v", decoded)
	}
}

func TestDecryptJSONRejectsMalformedEnvelopes(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"v2":              "v2:AA:BB:CC",
		"missing parts":   "v1:AA:BB",
		"bad base64url":   "v1:!!!!:!!!!:!!!!",
		"short iv":        "v1:AA:AAAAAAAAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAAAAAAAA",
		"tampered cipher": tamperEnvelope(t, vector1Envelope),
	}
	for name, envelope := range cases {
		var target map[string]any
		if err := DecryptJSON(cryptoTestSecret, envelope, &target); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestDecryptJSONTamperCopy(t *testing.T) {
	var target map[string]any
	err := DecryptJSON(cryptoTestSecret, tamperEnvelope(t, vector1Envelope), &target)
	if err == nil {
		t.Fatal("tampered envelope must fail")
	}
}
