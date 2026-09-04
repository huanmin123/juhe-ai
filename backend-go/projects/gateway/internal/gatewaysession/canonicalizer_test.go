package gatewaysession

import (
	"strings"
	"testing"
)

// The expected values were replayed with the Node implementation of
// versionedHmac (HMAC-SHA256 over JSON.stringify([domain, ...parts]),
// base64url) against the fixed secret below.
const testHMACSecret = "g14-test-secret"

func TestVersionedHMACReplayVectors(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		domain  string
		parts   []string
		prefix  string
		want    string
		wantErr error
	}{
		{
			name:   "conversation key",
			secret: testHMACSecret,
			domain: "conversation:v1",
			parts:  []string{"sys-1", "key-9", "openai.codex.session", "abc-session-123"},
			prefix: "conv_v1_",
			want:   "conv_v1_7DCcaz4omcf-A6GykddpbCv9_8Fu0sZv6V5I1BtZzsk",
		},
		{
			name:   "evidence key",
			secret: testHMACSecret,
			domain: "evidence:v1",
			parts:  []string{"sys-1", "key-9", "openai.codex.session", "abc-session-123"},
			prefix: "ev_v1_",
			want:   "ev_v1_YMo6DAR4NIuL_U0NE17RhMQuu6erIwqU34X_LUy16I0",
		},
		{
			name:   "affinity key",
			secret: testHMACSecret,
			domain: "affinity:v1",
			parts:  []string{"sys-1", "key-9", "conv-target", "rs-7", "grp-3", "ppp-5"},
			prefix: "aff_v1_",
			want:   "aff_v1_cxz8enf_7tT2pHY8Ynm7eLTRkYTcusiCnCvkTvL2SDM",
		},
		{
			name:   "affinity key internal/default placeholders",
			secret: testHMACSecret,
			domain: "affinity:v1",
			parts:  []string{"sys-1", "internal", "conv-target", "default", "grp-3", "default"},
			prefix: "aff_v1_",
			want:   "aff_v1_slLAGFC8gphVMtFakXEObWRbYq2mjC_PRY5fbvhw9Kc",
		},
		{
			name:   "JSON.stringify escaping (quote, backslash, newline, tab, unicode)",
			secret: testHMACSecret,
			domain: "evidence:v1",
			parts:  []string{"sys", "key", "ns", "q\"uote\\back\nnew\ttab 中文"},
			prefix: "ev_v1_",
			want:   "ev_v1_by5QCMWYYphJQJa1lXcSYbLd54X0mhKWxKCQqBzOYFE",
		},
		{
			name:   "control bytes in payload",
			secret: testHMACSecret,
			domain: "conversation:v1",
			parts:  []string{"sys", "internal", "ns", "a\x01b\x7f"},
			prefix: "conv_v1_",
			want:   "conv_v1_c7fknfKIR5vr7Y4HRccIjxJH7c721Q9tNg9a7Kf5faU",
		},
		{
			name:    "empty secret rejected",
			secret:  "   ",
			domain:  "affinity:v1",
			parts:   []string{"a"},
			prefix:  "aff_v1_",
			wantErr: ErrEmptyHMACSecret,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VersionedHMAC(tt.secret, tt.domain, tt.parts, tt.prefix)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("VersionedHMAC() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("VersionedHMAC() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("VersionedHMAC() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJsJSONStringArrayMatchesJSONStringify(t *testing.T) {
	tests := []struct {
		name  string
		items []string
		want  string
	}{
		{name: "empty", items: []string{}, want: "[]"},
		{name: "plain", items: []string{"a", "b"}, want: `["a","b"]`},
		{name: "escapes", items: []string{"a\"b\\c\nd\te\f\b", "中文"}, want: `["a\"b\\c\nd\te\f\b","中文"]`},
		// JSON.stringify renders control bytes as lowercase \u00xx.
		{name: "control bytes", items: []string{"a\x00\x1f\x7f"}, want: "[\"a\\u0000\\u001f" + string(rune(0x7f)) + "\"]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jsJSONStringArray(tt.items)
			if got != tt.want {
				t.Fatalf("jsJSONStringArray(%q) = %q, want %q", tt.items, got, tt.want)
			}
		})
	}
}

func TestValidateGatewaySessionIdentityCandidate(t *testing.T) {
	scope := ResolvedGatewaySessionIdentityScope{
		SystemAccountID: "sys-1",
		APIKeyID:        "key-9",
		HMACSecret:      testHMACSecret,
	}
	longValue := strings.Repeat("a", GatewaySessionIdentityMaxBytes+1)
	tests := []struct {
		name         string
		candidate    RawCandidate
		wantInvalid  IdentityInvalidReason
		wantRawValue string
		wantEvidence bool
	}{
		{
			name: "invalid shape",
			candidate: RawCandidate{
				ResolverID: "openai_codex_session_header", SemanticKind: IdentitySemanticKindSession,
				SemanticNamespace: CodexSessionSemanticNamespace,
				Source:            IdentityPhysicalSource{Location: IdentitySourceLocationHeader, Path: "session-id"},
				Confidence:        IdentityConfidenceAuthoritative, Priority: 600,
				InvalidShape: true,
			},
			wantInvalid: IdentityInvalidReasonInvalidShape,
		},
		{
			name:        "control character NUL",
			candidate:   rawSessionCandidate("a\x00b"),
			wantInvalid: IdentityInvalidReasonControlCharacter,
		},
		{
			name:        "control character unit separator",
			candidate:   rawSessionCandidate("a\x1fb"),
			wantInvalid: IdentityInvalidReasonControlCharacter,
		},
		{
			name:        "control character DEL",
			candidate:   rawSessionCandidate("a\x7fb"),
			wantInvalid: IdentityInvalidReasonControlCharacter,
		},
		{
			name:        "control character C1 range (U+009F)",
			candidate:   rawSessionCandidate("a" + string(rune(0x9F)) + "b"),
			wantInvalid: IdentityInvalidReasonControlCharacter,
		},
		{
			name:        "empty after trim",
			candidate:   rawSessionCandidate("   "),
			wantInvalid: IdentityInvalidReasonEmpty,
		},
		{
			name:        "too long",
			candidate:   rawSessionCandidate(longValue),
			wantInvalid: IdentityInvalidReasonTooLong,
		},
		{
			name:         "trimmed and canonicalized",
			candidate:    rawSessionCandidate("  abc-session  "),
			wantRawValue: "abc-session",
			wantEvidence: true,
		},
		{
			name:         "max bytes boundary ok (512 utf-8 bytes)",
			candidate:    rawSessionCandidate(strings.Repeat("é", GatewaySessionIdentityMaxBytes/2)),
			wantRawValue: strings.Repeat("é", GatewaySessionIdentityMaxBytes/2),
			wantEvidence: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validated, invalidReason, err := ValidateGatewaySessionIdentityCandidate(tt.candidate, scope)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantInvalid != "" {
				if validated != nil {
					t.Fatalf("expected invalid candidate, got %+v", validated)
				}
				if invalidReason != tt.wantInvalid {
					t.Fatalf("invalidReason = %q, want %q", invalidReason, tt.wantInvalid)
				}
				return
			}
			if validated == nil {
				t.Fatal("expected validated candidate")
			}
			if tt.wantRawValue != "" && validated.RawValue != tt.wantRawValue {
				t.Fatalf("rawValue = %q, want %q", validated.RawValue, tt.wantRawValue)
			}
			if tt.wantEvidence && !strings.HasPrefix(validated.EvidenceKey, "ev_v1_") {
				t.Fatalf("evidenceKey = %q, want ev_v1_ prefix", validated.EvidenceKey)
			}
		})
	}
}

func rawSessionCandidate(rawValue string) RawCandidate {
	return RawCandidate{
		ResolverID:        "openai_codex_session_header",
		SemanticKind:      IdentitySemanticKindSession,
		SemanticNamespace: CodexSessionSemanticNamespace,
		Source:            IdentityPhysicalSource{Location: IdentitySourceLocationHeader, Path: "session-id"},
		Confidence:        IdentityConfidenceAuthoritative,
		Priority:          600,
		RawValue:          rawValue,
	}
}

func TestCreateGatewayConversationKeyVector(t *testing.T) {
	scope := ResolvedGatewaySessionIdentityScope{
		SystemAccountID: "sys-1",
		APIKeyID:        "key-9",
		HMACSecret:      testHMACSecret,
	}
	got, err := CreateGatewayConversationKey(scope, "openai.codex.session", "abc-session-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "conv_v1_7DCcaz4omcf-A6GykddpbCv9_8Fu0sZv6V5I1BtZzsk"
	if got != want {
		t.Fatalf("CreateGatewayConversationKey() = %q, want %q", got, want)
	}
}

func TestDeriveGatewaySessionAffinityKeyFromConversationKeyVector(t *testing.T) {
	got, err := DeriveGatewaySessionAffinityKeyFromConversationKey("conv-target", GatewaySessionAffinityKeyScope{
		HMACSecret:                testHMACSecret,
		SystemAccountID:           "sys-1",
		APIKeyID:                  "key-9",
		RouteStrategyID:           "rs-7",
		GroupID:                   "grp-3",
		ProviderProtocolProfileID: "ppp-5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "aff_v1_cxz8enf_7tT2pHY8Ynm7eLTRkYTcusiCnCvkTvL2SDM"
	if got != want {
		t.Fatalf("DeriveGatewaySessionAffinityKeyFromConversationKey() = %q, want %q", got, want)
	}
}
