package secretcrypto_test

import (
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/secretcrypto"
)

func TestJSONCodecDecryptsNodeCompatibleFixture(t *testing.T) {
	codec := secretcrypto.NewJSONCodec("node-compatible-secret")

	got, err := codec.DecryptJSON("v1:AAECAwQFBgcICQoL:JUknIo37D7akWKp3ACPozg:3nMDwLXQOWUp8GcaqRPJy6jD29-TAhWj")
	if err != nil {
		t.Fatalf("DecryptJSON() error = %v", err)
	}
	if got["password"] != "p@ss word" {
		t.Fatalf("password = %#v, want %q", got["password"], "p@ss word")
	}
}

func TestJSONCodecUsesCurrentDefaultSecretForBlankInput(t *testing.T) {
	const fixture = "v1:AAECAwQFBgcICQoL:0bBUn6_vY6V7vEcAJ-SYMw:4bLvLJKzEcHRFOILEVjNhmVK0e5nf9hu"

	for _, secret := range []string{"", "   "} {
		codec := secretcrypto.NewJSONCodec(secret)
		got, err := codec.DecryptJSON(fixture)
		if err != nil {
			t.Fatalf("DecryptJSON() secret %q error = %v", secret, err)
		}
		if got["password"] != "p@ss word" {
			t.Fatalf("password with secret %q = %#v", secret, got["password"])
		}
	}
}

func TestJSONCodecEncryptsVersionedRoundTripPayload(t *testing.T) {
	codec := secretcrypto.NewJSONCodec("round-trip-secret")

	encrypted, err := codec.EncryptJSON(map[string]any{
		"password": "secret",
		"enabled":  true,
	})
	if err != nil {
		t.Fatalf("EncryptJSON() error = %v", err)
	}
	parts := strings.Split(encrypted, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("encrypted payload = %q, want v1:nonce:tag:ciphertext", encrypted)
	}
	if len(parts[1]) != 16 || len(parts[2]) != 22 || parts[3] == "" {
		t.Fatalf("encrypted components = %#v", parts)
	}

	got, err := codec.DecryptJSON(encrypted)
	if err != nil {
		t.Fatalf("DecryptJSON() error = %v", err)
	}
	if got["password"] != "secret" || got["enabled"] != true {
		t.Fatalf("round trip payload = %#v", got)
	}
}

func TestJSONCodecRejectsUnsupportedFormat(t *testing.T) {
	codec := secretcrypto.NewJSONCodec("secret")

	for _, value := range []string{"", "v2:a:b:c", "v1:::c"} {
		if _, err := codec.DecryptJSON(value); err == nil {
			t.Fatalf("DecryptJSON(%q) error = nil", value)
		}
	}
}
