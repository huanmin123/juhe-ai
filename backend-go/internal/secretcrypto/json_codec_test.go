package secretcrypto_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/secretcrypto"
)

func TestNewJSONCodecReturnsPointerToUnexportedConcreteType(t *testing.T) {
	codecType := reflect.TypeOf(secretcrypto.NewJSONCodec("secret"))
	if codecType.Kind() != reflect.Pointer {
		t.Fatalf("NewJSONCodec() type = %v, want pointer", codecType)
	}
	if concreteType := codecType.Elem(); concreteType.Name() == "" || token.IsExported(concreteType.Name()) {
		t.Fatalf("NewJSONCodec() concrete type = %v, want named unexported type", concreteType)
	}
}

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

func TestJSONCodecRejectsMalformedCiphertextWithoutPanic(t *testing.T) {
	const secret = "malformed-ciphertext-secret"
	codec := secretcrypto.NewJSONCodec(secret)
	valid, err := codec.EncryptJSON(map[string]any{"password": "secret"})
	if err != nil {
		t.Fatalf("EncryptJSON() error = %v", err)
	}

	tests := []struct {
		name  string
		value string
	}{
		{name: "valid base64 wrong nonce size", value: mutateEncryptedPart(t, valid, 1, truncateLastByte)},
		{name: "valid base64 wrong tag size", value: mutateEncryptedPart(t, valid, 2, truncateLastByte)},
		{name: "corrupted tag", value: mutateEncryptedPart(t, valid, 2, flipFirstByte)},
		{name: "corrupted ciphertext", value: mutateEncryptedPart(t, valid, 3, flipFirstByte)},
		{name: "authenticated non-object JSON", value: encryptRawJSON(t, secret, []byte("null"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				decryptErr error
				panicked   any
			)
			func() {
				defer func() {
					panicked = recover()
				}()
				_, decryptErr = codec.DecryptJSON(tt.value)
			}()
			if panicked != nil {
				t.Fatalf("DecryptJSON() panic = %v", panicked)
			}
			if decryptErr == nil {
				t.Fatal("DecryptJSON() error = nil")
			}
		})
	}
}

func mutateEncryptedPart(t *testing.T, value string, partIndex int, mutate func([]byte) []byte) string {
	t.Helper()
	parts := strings.Split(value, ":")
	if len(parts) != 4 {
		t.Fatalf("encrypted payload = %q, want four parts", value)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[partIndex])
	if err != nil {
		t.Fatalf("decode encrypted part %d: %v", partIndex, err)
	}
	parts[partIndex] = base64.RawURLEncoding.EncodeToString(mutate(decoded))
	return strings.Join(parts, ":")
}

func truncateLastByte(value []byte) []byte {
	return value[:len(value)-1]
}

func flipFirstByte(value []byte) []byte {
	value[0] ^= 0xff
	return value
}

func encryptRawJSON(t *testing.T, secret string, plain []byte) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("aes.NewCipher() error = %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM() error = %v", err)
	}
	nonce := make([]byte, aead.NonceSize())
	sealed := aead.Seal(nil, nonce, plain, nil)
	ciphertext := sealed[:len(sealed)-aead.Overhead()]
	tag := sealed[len(sealed)-aead.Overhead():]
	encode := base64.RawURLEncoding.EncodeToString
	return "v1:" + encode(nonce) + ":" + encode(tag) + ":" + encode(ciphertext)
}
