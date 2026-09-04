package gatewaycodex

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// Port of client-profiles/source-identity.ts.
//
// Resolves the one internal source identifier consumed by source avoidance
// and availability-probe fencing. Its session result is also reused by the
// separate session-affinity path. Individual profiles only contribute their
// official, protocol-specific evidence.

// GatewayClientSourceKind mirrors GatewayClientSourceKind.
type GatewayClientSourceKind = string

// Client source kinds.
const (
	SourceKindOfficialSession  = "official_session"
	SourceKindProtocolResource = "protocol_resource"
	SourceKindIPAPIKeyFallback = "ip_api_key_fallback"
)

// GatewayClientSourceStatus mirrors GatewayClientSourceStatus.
type GatewayClientSourceStatus = string

// Client source statuses.
const (
	SourceStatusResolved = "resolved"
	SourceStatusMissing  = "missing"
	SourceStatusInvalid  = "invalid"
	SourceStatusConflict = "conflict"
)

// GatewayClientSourceIdentity mirrors GatewayClientSourceIdentity.
type GatewayClientSourceIdentity struct {
	Status    GatewayClientSourceStatus
	SourceKey string
	// AffinityKey: only stable protocol evidence may participate in session
	// affinity; the IP/API-Key fallback never sets it.
	AffinityKey       string
	Kind              GatewayClientSourceKind
	SemanticNamespace string
	// SessionIdentity is kept request-local so preflight and source handling
	// share one resolver result. Only the HMAC source key may enter runtime
	// state.
	SessionIdentity *gatewaysession.GatewaySessionIdentity
}

// GatewayClientSourceIdentityInput mirrors GatewayClientSourceIdentityInput.
type GatewayClientSourceIdentityInput struct {
	ClientProfile       string
	ClientProfileSource string
	DownstreamProtocol  string
	SystemAccountID     string
	APIKeyID            string
	ClientIP            string
}

// SessionIdentityResolver is the seam toward session-identity (G14). The Go
// gatewaysession.IdentityService satisfies it through the package adapter.
type SessionIdentityResolver interface {
	ResolveSessionIdentity(req *gatewaypreauth.GatewayRequest, input gatewaypreauth.SessionIdentityInput) gatewaysession.GatewaySessionIdentity
}

// GeminiInteractionResourceIDFunc mirrors the optional
// geminiInteractionResourceIdFromRequest dependency (gemini slice); nil
// reads as "no interaction id".
type GeminiInteractionResourceIDFunc func(req *gatewaypreauth.GatewayRequest) string

// SourceIdentityResolver carries the source-identity dependencies.
type SourceIdentityResolver struct {
	Secret                      string
	Session                     SessionIdentityResolver
	GeminiInteractionResourceID GeminiInteractionResourceIDFunc
}

// ResolveGatewayClientSourceIdentity mirrors
// resolveGatewayClientSourceIdentity.
func (r *SourceIdentityResolver) ResolveGatewayClientSourceIdentity(req *gatewaypreauth.GatewayRequest, input GatewayClientSourceIdentityInput) GatewayClientSourceIdentity {
	systemAccountID := requiredPart(input.SystemAccountID)
	apiKeyID := requiredPart(input.APIKeyID)
	if systemAccountID == "" || apiKeyID == "" {
		return GatewayClientSourceIdentity{Status: SourceStatusMissing}
	}

	var sessionIdentity *gatewaysession.GatewaySessionIdentity
	if profileMayUseOfficialSession(input) {
		resolved := r.resolveSessionIdentity(req, input, systemAccountID, apiKeyID)
		sessionIdentity = &resolved
		if resolved.Status == gatewaysession.IdentityStatusResolved && resolved.ConversationKey != "" && resolved.SemanticNamespace != "" {
			source := resolvedSource(r.Secret, sourceResolvedInput{
				kind:              SourceKindOfficialSession,
				systemAccountID:   systemAccountID,
				apiKeyID:          apiKeyID,
				semanticNamespace: resolved.SemanticNamespace,
				stableValue:       resolved.ConversationKey,
				affinityKey:       resolved.ConversationKey,
			})
			source.SessionIdentity = sessionIdentity
			return source
		}
		if resolved.Status == gatewaysession.IdentityStatusInvalid || resolved.Status == gatewaysession.IdentityStatusConflict {
			return GatewayClientSourceIdentity{Status: GatewayClientSourceStatus(resolved.Status), SessionIdentity: sessionIdentity}
		}
	}

	// Gemini Interactions has no session header. A returned interaction
	// resource is nevertheless a stable protocol identity for
	// follow-up/cancel requests.
	interactionID := ""
	if r.GeminiInteractionResourceID != nil {
		interactionID = r.GeminiInteractionResourceID(req)
	}
	if interactionID != "" && strings.HasPrefix(input.DownstreamProtocol, "gemini_interactions") {
		source := resolvedSource(r.Secret, sourceResolvedInput{
			kind:              SourceKindProtocolResource,
			systemAccountID:   systemAccountID,
			apiKeyID:          apiKeyID,
			semanticNamespace: "google.gemini.interaction",
			stableValue:       interactionID,
			affinityKey:       interactionID,
		})
		source.SessionIdentity = sessionIdentity
		return source
	}

	clientIP := requiredPart(input.ClientIP)
	if clientIP == "" {
		return GatewayClientSourceIdentity{Status: SourceStatusMissing, SessionIdentity: sessionIdentity}
	}
	source := resolvedSource(r.Secret, sourceResolvedInput{
		kind:              SourceKindIPAPIKeyFallback,
		systemAccountID:   systemAccountID,
		apiKeyID:          apiKeyID,
		semanticNamespace: "gateway.ip_api_key",
		stableValue:       clientIP,
	})
	source.SessionIdentity = sessionIdentity
	return source
}

func (r *SourceIdentityResolver) resolveSessionIdentity(req *gatewaypreauth.GatewayRequest, input GatewayClientSourceIdentityInput, systemAccountID, apiKeyID string) gatewaysession.GatewaySessionIdentity {
	if r.Session == nil {
		return gatewaysession.GatewaySessionIdentity{Status: gatewaysession.IdentityStatusMissing}
	}
	return r.Session.ResolveSessionIdentity(req, gatewaypreauth.SessionIdentityInput{
		ClientProfile:   input.ClientProfile,
		SystemAccountID: systemAccountID,
		APIKeyID:        apiKeyID,
	})
}

// DeriveGatewayClientSourceStateKey mirrors deriveGatewayClientSourceStateKey:
// narrows a source identity to a dispatch surface without exposing raw IDs.
func (r *SourceIdentityResolver) DeriveGatewayClientSourceStateKey(source GatewayClientSourceIdentity, input struct {
	ClientProfile      string
	Endpoint           string
	DownstreamProtocol string
}) string {
	if source.SourceKey == "" {
		return ""
	}
	endpoint := requiredPart(input.Endpoint)
	if endpoint == "" {
		return ""
	}
	return gatewayClientSourceHMAC(r.Secret, "state:v1", []string{source.SourceKey, input.ClientProfile, endpoint, input.DownstreamProtocol})
}

// DeriveGatewayClientSourceChildStateKey mirrors
// deriveGatewayClientSourceChildStateKey.
func (r *SourceIdentityResolver) DeriveGatewayClientSourceChildStateKey(sourceStateKey, childKind, childID string) string {
	normalizedStateKey := requiredPart(sourceStateKey)
	normalizedChildID := requiredPart(childID)
	if normalizedStateKey == "" || normalizedChildID == "" {
		return ""
	}
	return gatewayClientSourceHMAC(r.Secret, "child:v1", []string{normalizedStateKey, childKind, normalizedChildID})
}

type sourceResolvedInput struct {
	kind              string
	systemAccountID   string
	apiKeyID          string
	semanticNamespace string
	stableValue       string
	affinityKey       string
}

func resolvedSource(secret string, input sourceResolvedInput) GatewayClientSourceIdentity {
	identity := GatewayClientSourceIdentity{
		Status:            SourceStatusResolved,
		Kind:              input.kind,
		SemanticNamespace: input.semanticNamespace,
	}
	if input.affinityKey != "" {
		identity.AffinityKey = input.affinityKey
	}
	identity.SourceKey = gatewayClientSourceHMAC(secret, "source:v1", []string{
		input.systemAccountID,
		input.apiKeyID,
		input.kind,
		input.semanticNamespace,
		input.stableValue,
	})
	return identity
}

func profileMayUseOfficialSession(input GatewayClientSourceIdentityInput) bool {
	if input.ClientProfile == "codex" {
		return input.ClientProfileSource == "codex_turn_metadata"
	}
	if input.ClientProfile == "claude_code" {
		return input.ClientProfileSource == "claude_code_request_signature"
	}
	return false
}

func requiredPart(value string) string {
	return strings.TrimSpace(value)
}

// gatewayClientSourceHMAC mirrors the Node hmac(domain, parts) helper: HMAC
// over the JSON array [domain, ...parts] with a base64url digest and the
// src_v1_ prefix.
func gatewayClientSourceHMAC(secret, domain string, parts []string) string {
	normalizedSecret := strings.TrimSpace(secret)
	if normalizedSecret == "" {
		panic(errors.New("Gateway client source identity requires a configured HMAC secret"))
	}
	payload := jsJSONArray(append([]string{domain}, parts...))
	mac := hmac.New(sha256.New, []byte(normalizedSecret))
	mac.Write([]byte(payload))
	return "src_v1_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// jsJSONArray mirrors JSON.stringify of a string array: double quotes, the
// two mandatory escapes and short escapes for control characters; every
// other code point stays raw UTF-8.
func jsJSONArray(values []string) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('"')
		for _, r := range value {
			switch r {
			case '"':
				builder.WriteString(`\"`)
			case '\\':
				builder.WriteString(`\\`)
			case '\n':
				builder.WriteString(`\n`)
			case '\r':
				builder.WriteString(`\r`)
			case '\t':
				builder.WriteString(`\t`)
			case '\b':
				builder.WriteString(`\b`)
			case '\f':
				builder.WriteString(`\f`)
			default:
				if r < 0x20 {
					builder.WriteString(`\u`)
					const hexDigits = "0123456789abcdef"
					builder.WriteByte('0')
					builder.WriteByte('0')
					builder.WriteByte(hexDigits[(r>>4)&0xf])
					builder.WriteByte(hexDigits[r&0xf])
					continue
				}
				builder.WriteRune(r)
			}
		}
		builder.WriteByte('"')
	}
	builder.WriteByte(']')
	return builder.String()
}
