package gatewaycredentials_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycredentials"
)

func TestExtractSelectsCredentialsInStablePriorityOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      gatewaycredentials.Input
		wantSecret string
		wantSource gatewaycredentials.Source
	}{
		{
			name: "bearer wins over every lower priority source",
			input: gatewaycredentials.Input{
				Authorization:   []string{"  bEaReR bearer-secret  "},
				XAPIKey:         []string{"x-api-key-secret"},
				GeminiHeaderKey: []string{"gemini-header-secret"},
				GeminiQueryKey:  []string{"gemini-query-secret"},
				GeminiNative:    true,
			},
			wantSecret: "bearer-secret",
			wantSource: gatewaycredentials.SourceBearer,
		},
		{
			name: "x api key wins over Gemini sources",
			input: gatewaycredentials.Input{
				XAPIKey:         []string{"  x-api-key-secret  "},
				GeminiHeaderKey: []string{"gemini-header-secret"},
				GeminiQueryKey:  []string{"gemini-query-secret"},
				GeminiNative:    true,
			},
			wantSecret: "x-api-key-secret",
			wantSource: gatewaycredentials.SourceXAPIKey,
		},
		{
			name: "Gemini header wins over Gemini query key",
			input: gatewaycredentials.Input{
				GeminiHeaderKey: []string{"gemini-header-secret"},
				GeminiQueryKey:  []string{"gemini-query-secret"},
				GeminiNative:    true,
			},
			wantSecret: "gemini-header-secret",
			wantSource: gatewaycredentials.SourceGeminiHeader,
		},
		{
			name: "Gemini query key is the final eligible source",
			input: gatewaycredentials.Input{
				GeminiQueryKey: []string{"gemini-query-secret"},
				GeminiNative:   true,
			},
			wantSecret: "gemini-query-secret",
			wantSource: gatewaycredentials.SourceGeminiQuery,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := gatewaycredentials.Extract(tt.input)
			if err != nil {
				t.Fatalf("Extract() error = %v", err)
			}
			if got.Secret() != tt.wantSecret || got.Source != tt.wantSource {
				t.Fatalf("Extract() source = %q, secret matches = %t", got.Source, got.Secret() == tt.wantSecret)
			}
		})
	}
}

func TestExtractDoesNotUseGeminiSourcesForNonNativeRequests(t *testing.T) {
	t.Parallel()

	_, err := gatewaycredentials.Extract(gatewaycredentials.Input{
		GeminiHeaderKey: []string{"gemini-header-secret"},
		GeminiQueryKey:  []string{"gemini-query-secret"},
		GeminiNative:    false,
	})
	if !errors.Is(err, gatewaycredentials.ErrMissingCredential) {
		t.Fatalf("Extract() error = %v, want ErrMissingCredential", err)
	}
}

func TestExtractRejectsMalformedAndAmbiguousCredentialSourcesWithoutSecretLeakage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   gatewaycredentials.Input
		wantErr error
		secret  string
	}{
		{
			name:    "malformed bearer does not fall through to x api key",
			input:   gatewaycredentials.Input{Authorization: []string{"Basic bearer-secret"}, XAPIKey: []string{"x-api-key-secret"}},
			wantErr: gatewaycredentials.ErrMalformedCredential,
			secret:  "bearer-secret",
		},
		{
			name:    "bearer secret cannot contain whitespace",
			input:   gatewaycredentials.Input{Authorization: []string{"Bearer before after"}},
			wantErr: gatewaycredentials.ErrMalformedCredential,
			secret:  "before after",
		},
		{
			name:    "bearer scheme cannot use a control character separator",
			input:   gatewaycredentials.Input{Authorization: []string{"Bearer\nsecret"}},
			wantErr: gatewaycredentials.ErrMalformedCredential,
			secret:  "secret",
		},
		{
			name:    "coalesced bearer values are ambiguous",
			input:   gatewaycredentials.Input{Authorization: []string{"Bearer first-secret,Bearer-second-secret"}},
			wantErr: gatewaycredentials.ErrAmbiguousCredential,
			secret:  "first-secret",
		},
		{
			name:    "x api key cannot contain a control character",
			input:   gatewaycredentials.Input{XAPIKey: []string{"x-api-key\nsecret"}},
			wantErr: gatewaycredentials.ErrMalformedCredential,
			secret:  "x-api-key-secret",
		},
		{
			name:    "repeated x api key values are ambiguous",
			input:   gatewaycredentials.Input{XAPIKey: []string{"first-secret", "second-secret"}},
			wantErr: gatewaycredentials.ErrAmbiguousCredential,
			secret:  "first-secret",
		},
		{
			name:    "repeated source with a blank value is ambiguous",
			input:   gatewaycredentials.Input{XAPIKey: []string{"", "second-secret"}},
			wantErr: gatewaycredentials.ErrAmbiguousCredential,
			secret:  "second-secret",
		},
		{
			name:    "coalesced header values are ambiguous",
			input:   gatewaycredentials.Input{XAPIKey: []string{"first-secret,second-secret"}},
			wantErr: gatewaycredentials.ErrAmbiguousCredential,
			secret:  "first-secret",
		},
		{
			name:    "credential has a finite size boundary",
			input:   gatewaycredentials.Input{XAPIKey: []string{strings.Repeat("s", 4097)}},
			wantErr: gatewaycredentials.ErrMalformedCredential,
			secret:  strings.Repeat("s", 4097),
		},
		{
			name:    "repeated Gemini query values are ambiguous",
			input:   gatewaycredentials.Input{GeminiQueryKey: []string{"first-secret", "second-secret"}, GeminiNative: true},
			wantErr: gatewaycredentials.ErrAmbiguousCredential,
			secret:  "second-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := gatewaycredentials.Extract(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Extract() error = %v, want %v", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), tt.secret) {
				t.Fatalf("Extract() leaked credential in error: %q", err)
			}
		})
	}
}

func TestCredentialFormattingAndJSONDoNotExposeSecret(t *testing.T) {
	t.Parallel()

	const secret = "credential-must-stay-private"
	credential, err := gatewaycredentials.Extract(gatewaycredentials.Input{XAPIKey: []string{secret}})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}

	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	outputs := []string{
		fmt.Sprint(credential),
		fmt.Sprintf("%+v", credential),
		fmt.Sprintf("%#v", credential),
		string(encoded),
	}
	for _, output := range outputs {
		if strings.Contains(output, secret) {
			t.Fatalf("credential leaked through formatting or JSON: %q", output)
		}
	}
	if credential.Secret() != secret {
		t.Fatal("Secret() did not return the selected credential")
	}
}
