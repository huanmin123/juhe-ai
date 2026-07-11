package apikeysecret_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/apikeysecret"
)

func TestGenerateReturnsSkPrefixed32ByteHexSecret(t *testing.T) {
	first, err := apikeysecret.Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	second, err := apikeysecret.Generate()
	if err != nil {
		t.Fatalf("Generate() second error = %v", err)
	}

	if !strings.HasPrefix(first, "sk-") || len(first) != 67 {
		t.Fatalf("secret = %q, want sk- plus 64 hex characters", first)
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(first, "sk-"))
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded length = %d, want 32", len(decoded))
	}
	if first == second {
		t.Fatalf("two generated secrets matched: %q", first)
	}
}

func TestHashReturnsSHA256Hex(t *testing.T) {
	const secret = "sk-0123456789abcdef"
	const want = "221df437bc4f8c4d9718bd7a9ec4c1021c72fd69dfb2a5f568ecf4607c824bee"

	if got := apikeysecret.Hash(secret); got != want {
		t.Fatalf("Hash() = %q, want %q", got, want)
	}
}

func TestPrefixAndSuffixReturnEightCharacterMarkers(t *testing.T) {
	const secret = "sk-0123456789abcdef0123456789abcdef"

	if got := apikeysecret.Prefix(secret); got != "sk-01234" {
		t.Fatalf("Prefix() = %q, want %q", got, "sk-01234")
	}
	if got := apikeysecret.Suffix(secret); got != "89abcdef" {
		t.Fatalf("Suffix() = %q, want %q", got, "89abcdef")
	}
	for _, short := range []string{"", "sk-test"} {
		if got := apikeysecret.Prefix(short); got != short {
			t.Fatalf("Prefix(%q) = %q", short, got)
		}
		if got := apikeysecret.Suffix(short); got != short {
			t.Fatalf("Suffix(%q) = %q", short, got)
		}
	}
}
