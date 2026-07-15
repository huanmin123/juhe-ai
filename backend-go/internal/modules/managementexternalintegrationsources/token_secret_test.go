package managementexternalintegrationsources

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	tokenSecretTestSecret = "management-external-integration-source-token-secret-test"
	// Produced by Node.js AES-256-GCM with nonce 000102030405060708090a0b and JSON {"token":"sk-external-revealed"}.
	nodeTokenSecretFixture = "v1:AAECAwQFBgcICQoL:C3ib4Bh55ctnpp_Jw-ceqQ:1KtHuCjNvTTmtm4gILTJyZSKDn-Skdt0QeTaenzWdFc"
)

func TestServiceRevealTokenSecretTrimsECMAScriptWhitespaceAndDecryptsNodePayload(t *testing.T) {
	reader := &externalIntegrationSourceTokenSecretReaderStub{
		encrypted: nodeTokenSecretFixture,
		found:     true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		SecretReader: reader,
		Secret:       tokenSecretTestSecret,
	})

	got, err := service.RevealTokenSecret(
		context.Background(),
		"\uFEFF \tsource_1\r\n",
		"\u00A0token_1\uFEFF",
	)
	if err != nil {
		t.Fatalf("RevealTokenSecret() error = %v", err)
	}
	if got == nil || got.Token != "sk-external-revealed" {
		t.Fatalf("RevealTokenSecret() = %#v", got)
	}
	wantCalls := []tokenSecretReaderCall{{sourceID: "source_1", tokenID: "token_1"}}
	if !reflect.DeepEqual(reader.calls, wantCalls) {
		t.Fatalf("reader calls = %#v, want %#v", reader.calls, wantCalls)
	}
}

func TestServiceRevealTokenSecretPreservesAnyNonemptyTokenExactly(t *testing.T) {
	codec := secretcrypto.NewJSONCodec(tokenSecretTestSecret)
	for _, token := range []string{" ", "  arbitrary token without prefix  "} {
		t.Run(token, func(t *testing.T) {
			encrypted, err := codec.EncryptJSON(map[string]any{"token": token})
			if err != nil {
				t.Fatalf("encrypt fixture: %v", err)
			}
			reader := &externalIntegrationSourceTokenSecretReaderStub{encrypted: encrypted, found: true}
			service := NewServiceWithOptions(ServiceOptions{SecretReader: reader, Secret: tokenSecretTestSecret})

			got, err := service.RevealTokenSecret(context.Background(), "source_1", "token_1")
			if err != nil {
				t.Fatalf("RevealTokenSecret() error = %v", err)
			}
			if got == nil || got.Token != token {
				t.Fatalf("RevealTokenSecret() = %#v, want exact token %q", got, token)
			}
		})
	}
}

func TestNewServiceDoesNotEnableSecretRevealWithoutExplicitSecret(t *testing.T) {
	reader := &externalIntegrationSourceTokenSecretReaderStub{
		encrypted: nodeTokenSecretFixture,
		found:     true,
	}
	store := &externalIntegrationSourceCombinedStore{
		externalIntegrationSourceStoreStub:             &externalIntegrationSourceStoreStub{},
		externalIntegrationSourceTokenSecretReaderStub: reader,
	}

	got, err := NewService(store).RevealTokenSecret(context.Background(), "source_1", "token_1")
	if got != nil || err == nil || !strings.Contains(err.Error(), "token secret reader is required") {
		t.Fatalf("RevealTokenSecret() = %#v, error = %v", got, err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("legacy constructor must not enable secret reader, calls = %#v", reader.calls)
	}
}

func TestNewServiceWithOptionsDoesNotEnableSecretRevealWithBlankSecret(t *testing.T) {
	for _, secret := range []string{"", " \t\r\n "} {
		t.Run(secret, func(t *testing.T) {
			reader := &externalIntegrationSourceTokenSecretReaderStub{
				encrypted: nodeTokenSecretFixture,
				found:     true,
			}
			service := NewServiceWithOptions(ServiceOptions{
				SecretReader: reader,
				Secret:       secret,
			})

			got, err := service.RevealTokenSecret(context.Background(), "source_1", "token_1")
			if got != nil || err == nil || !strings.Contains(err.Error(), "token secret reader is required") {
				t.Fatalf("RevealTokenSecret() = %#v, error = %v", got, err)
			}
			if len(reader.calls) != 0 {
				t.Fatalf("blank secret must not enable secret reader, calls = %#v", reader.calls)
			}
		})
	}
}

func TestServiceRevealTokenSecretPreservesU0085InIdentifiers(t *testing.T) {
	reader := &externalIntegrationSourceTokenSecretReaderStub{found: false}
	service := NewServiceWithOptions(ServiceOptions{SecretReader: reader, Secret: tokenSecretTestSecret})
	const sourceID = "\u0085source_1\u0085"
	const tokenID = "\u0085token_1\u0085"

	got, err := service.RevealTokenSecret(context.Background(), sourceID, tokenID)
	if err != nil || got != nil {
		t.Fatalf("RevealTokenSecret() = %#v, error = %v", got, err)
	}
	wantCalls := []tokenSecretReaderCall{{sourceID: sourceID, tokenID: tokenID}}
	if !reflect.DeepEqual(reader.calls, wantCalls) {
		t.Fatalf("reader calls = %#v, want %#v", reader.calls, wantCalls)
	}
}

func TestServiceRevealTokenSecretSkipsLookupForEmptyIdentifiers(t *testing.T) {
	tests := []struct {
		name     string
		sourceID string
		tokenID  string
	}{
		{name: "empty source ID", sourceID: " \t\r\n\uFEFF", tokenID: "token_1"},
		{name: "empty token ID", sourceID: "source_1", tokenID: "\u00A0\uFEFF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &externalIntegrationSourceTokenSecretReaderStub{err: errors.New("must not query")}
			service := NewServiceWithOptions(ServiceOptions{SecretReader: reader, Secret: tokenSecretTestSecret})

			got, err := service.RevealTokenSecret(context.Background(), tt.sourceID, tt.tokenID)
			if err != nil || got != nil {
				t.Fatalf("RevealTokenSecret() = %#v, error = %v", got, err)
			}
			if len(reader.calls) != 0 {
				t.Fatalf("empty identifier must not query, calls = %#v", reader.calls)
			}
		})
	}
}

func TestServiceRevealTokenSecretDoesNotApplyTokenStatusPolicy(t *testing.T) {
	// The reader deliberately exposes ciphertext only, so disabled, revoked, and
	// expired records have the same reveal behavior as any other matching row.
	for _, status := range []string{"disabled", "revoked", "expired"} {
		t.Run(status, func(t *testing.T) {
			reader := &externalIntegrationSourceTokenSecretReaderStub{encrypted: nodeTokenSecretFixture, found: true}
			service := NewServiceWithOptions(ServiceOptions{SecretReader: reader, Secret: tokenSecretTestSecret})

			got, err := service.RevealTokenSecret(context.Background(), "source_1", "token_1")
			if err != nil {
				t.Fatalf("RevealTokenSecret() error = %v", err)
			}
			if got == nil || got.Token != "sk-external-revealed" {
				t.Fatalf("RevealTokenSecret() = %#v", got)
			}
		})
	}
}

func TestServiceRevealTokenSecretRejectsUnavailableOrInvalidSecrets(t *testing.T) {
	codec := secretcrypto.NewJSONCodec(tokenSecretTestSecret)
	tests := []struct {
		name       string
		encrypted  string
		payload    map[string]any
		usePayload bool
	}{
		{name: "missing ciphertext"},
		{name: "blank ciphertext", encrypted: " \t\r\n"},
		{name: "decrypt failure", encrypted: "not-ciphertext"},
		{name: "missing token field", payload: map[string]any{}, usePayload: true},
		{name: "non-string token", payload: map[string]any{"token": 42}, usePayload: true},
		{name: "empty token", payload: map[string]any{"token": ""}, usePayload: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encrypted := tt.encrypted
			if tt.usePayload {
				var err error
				encrypted, err = codec.EncryptJSON(tt.payload)
				if err != nil {
					t.Fatalf("encrypt fixture: %v", err)
				}
			}
			reader := &externalIntegrationSourceTokenSecretReaderStub{encrypted: encrypted, found: true}
			service := NewServiceWithOptions(ServiceOptions{SecretReader: reader, Secret: tokenSecretTestSecret})

			got, err := service.RevealTokenSecret(context.Background(), "source_1", "token_1")
			if err == nil {
				t.Fatalf("RevealTokenSecret() = %#v, error = nil", got)
			}
			if got != nil {
				t.Fatalf("RevealTokenSecret() = %#v, want nil on error", got)
			}
		})
	}
}

func TestServiceRevealTokenSecretReturnsNilForMissingRowAndPropagatesReaderError(t *testing.T) {
	t.Run("missing row", func(t *testing.T) {
		reader := &externalIntegrationSourceTokenSecretReaderStub{}
		service := NewServiceWithOptions(ServiceOptions{SecretReader: reader, Secret: tokenSecretTestSecret})

		got, err := service.RevealTokenSecret(context.Background(), "source_1", "token_1")
		if err != nil || got != nil {
			t.Fatalf("RevealTokenSecret() = %#v, error = %v", got, err)
		}
	})

	t.Run("reader error", func(t *testing.T) {
		wantErr := errors.New("token secret query failed")
		reader := &externalIntegrationSourceTokenSecretReaderStub{err: wantErr}
		service := NewServiceWithOptions(ServiceOptions{SecretReader: reader, Secret: tokenSecretTestSecret})

		got, err := service.RevealTokenSecret(context.Background(), "source_1", "token_1")
		if got != nil || !errors.Is(err, wantErr) {
			t.Fatalf("RevealTokenSecret() = %#v, error = %v, want %v", got, err, wantErr)
		}
	})
}

type tokenSecretReaderCall struct {
	sourceID string
	tokenID  string
}

type externalIntegrationSourceTokenSecretReaderStub struct {
	encrypted string
	found     bool
	err       error
	calls     []tokenSecretReaderCall
}

func (s *externalIntegrationSourceTokenSecretReaderStub) FindManagementExternalIntegrationSourceTokenSecret(
	_ context.Context,
	sourceID string,
	tokenID string,
) (string, bool, error) {
	s.calls = append(s.calls, tokenSecretReaderCall{sourceID: sourceID, tokenID: tokenID})
	return s.encrypted, s.found, s.err
}

var _ port.ManagementExternalIntegrationSourceTokenSecretReader = (*externalIntegrationSourceTokenSecretReaderStub)(nil)

type externalIntegrationSourceCombinedStore struct {
	*externalIntegrationSourceStoreStub
	*externalIntegrationSourceTokenSecretReaderStub
}
