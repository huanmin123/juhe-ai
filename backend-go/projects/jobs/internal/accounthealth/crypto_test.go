package accounthealth

import (
	"bytes"
	"testing"
)

func TestV1EnvelopeRoundTrip(t *testing.T) {
	secret := "shared-j1-secret"
	plaintext := []byte(`{"url":"socks5h://user:password@example.test:1080"}`)
	envelope, err := EncryptV1Envelope(secret, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecryptV1Envelope(secret, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plaintext) {
		t.Fatalf("round trip plaintext = %q", decoded)
	}
	if _, err := DecryptV1Envelope("wrong-secret", envelope); err == nil {
		t.Fatal("expected secret mismatch to fail")
	}
}
