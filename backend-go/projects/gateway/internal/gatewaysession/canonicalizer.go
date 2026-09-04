package gatewaysession

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// GatewaySessionIdentityMaxBytes mirrors gatewaySessionIdentityMaxBytes.
const GatewaySessionIdentityMaxBytes = 512

// Affinity/identity HMAC domain markers and prefixes, byte-identical to the
// Node canonicalizer.
const (
	hmacDomainEvidence     = "evidence:v1"
	hmacDomainConversation = "conversation:v1"
	hmacDomainAffinity     = "affinity:v1"

	prefixEvidence     = "ev_v1_"
	prefixConversation = "conv_v1_"
	prefixAffinity     = "aff_v1_"

	// scopeInternalPart mirrors `scope.apiKeyId ?? 'internal'`.
	scopeInternalPart = "internal"
	// scopeDefaultPart mirrors `scope.routeStrategyId ?? 'default'` and
	// `scope.providerProtocolProfileId ?? 'default'`.
	scopeDefaultPart = "default"
)

// ErrEmptyHMACSecret mirrors the Node versionedHmac guard.
var ErrEmptyHMACSecret = errors.New("Gateway session identity HMAC secret must not be empty")

// VersionedHMAC mirrors versionedHmac: HMAC-SHA256 over the JSON.stringify
// payload `[domain, ...parts]`, base64url digested, prefixed. The JSON
// encoding must match JSON.stringify byte-for-byte (see jsJSONString).
func VersionedHMAC(secret string, domain string, parts []string, prefix string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", ErrEmptyHMACSecret
	}
	payload := jsJSONStringArray(append([]string{domain}, parts...))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return prefix + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// ValidateGatewaySessionIdentityCandidate mirrors
// validateGatewaySessionIdentityCandidate. Exactly one of the results is
// non-zero: a validated candidate (trimmed raw value + evidence key) or an
// invalid reason.
func ValidateGatewaySessionIdentityCandidate(
	candidate RawCandidate,
	scope ResolvedGatewaySessionIdentityScope,
) (validated *ValidatedGatewaySessionIdentityCandidate, invalidReason IdentityInvalidReason, err error) {
	if candidate.InvalidShape {
		return nil, IdentityInvalidReasonInvalidShape, nil
	}
	if hasControlCharacter(candidate.RawValue) {
		return nil, IdentityInvalidReasonControlCharacter, nil
	}
	rawValue := jsTrimString(candidate.RawValue)
	if rawValue == "" {
		return nil, IdentityInvalidReasonEmpty, nil
	}
	if len(rawValue) > GatewaySessionIdentityMaxBytes {
		return nil, IdentityInvalidReasonTooLong, nil
	}
	evidenceKey, err := canonicalizeGatewayIdentityValue(scope, hmacDomainEvidence, candidate.SemanticNamespace, rawValue, prefixEvidence)
	if err != nil {
		return nil, "", err
	}
	return &ValidatedGatewaySessionIdentityCandidate{
		RawCandidate: RawCandidate{
			ResolverID:        candidate.ResolverID,
			SemanticKind:      candidate.SemanticKind,
			SemanticNamespace: candidate.SemanticNamespace,
			Source:            candidate.Source,
			Confidence:        candidate.Confidence,
			Priority:          candidate.Priority,
			RawValue:          rawValue,
		},
		EvidenceKey: evidenceKey,
	}, "", nil
}

// CreateGatewayConversationKey mirrors createGatewayConversationKey.
func CreateGatewayConversationKey(scope ResolvedGatewaySessionIdentityScope, semanticNamespace string, rawSessionID string) (string, error) {
	return canonicalizeGatewayIdentityValue(scope, hmacDomainConversation, semanticNamespace, rawSessionID, prefixConversation)
}

// DeriveGatewaySessionAffinityKeyFromConversationKey mirrors
// deriveGatewaySessionAffinityKeyFromConversationKey. The scope field order is
// contractual:
// [systemAccountId, apiKeyId ?? 'internal', conversationKey,
//
//	routeStrategyId ?? 'default', groupId, providerProtocolProfileId ?? 'default'].
func DeriveGatewaySessionAffinityKeyFromConversationKey(
	conversationKey string,
	scope GatewaySessionAffinityKeyScope,
) (string, error) {
	return VersionedHMAC(scope.HMACSecret, hmacDomainAffinity, []string{
		scope.SystemAccountID,
		orDefault(scope.APIKeyID, scopeInternalPart),
		conversationKey,
		orDefault(scope.RouteStrategyID, scopeDefaultPart),
		scope.GroupID,
		orDefault(scope.ProviderProtocolProfileID, scopeDefaultPart),
	}, prefixAffinity)
}

func canonicalizeGatewayIdentityValue(
	scope ResolvedGatewaySessionIdentityScope,
	domain string,
	semanticNamespace string,
	rawValue string,
	prefix string,
) (string, error) {
	return VersionedHMAC(scope.HMACSecret, domain, []string{
		scope.SystemAccountID,
		orDefault(scope.APIKeyID, scopeInternalPart),
		semanticNamespace,
		rawValue,
	}, prefix)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// jsJSONStringArray serializes []string exactly like JSON.stringify for valid
// UTF-8 input: compact separators, double quotes, and the JSON.stringify
// escape table (\b \f \n \r \t shorthands, \u00xx for other control bytes).
// encoding/json is not used because it HTML-escapes < > & and renders \b/\f
// as \u0008/\u000c, which would change the HMAC payload.
func jsJSONStringArray(items []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		jsJSONStringInto(&b, item)
	}
	b.WriteByte(']')
	return b.String()
}

func jsJSONString(s string) string {
	var b strings.Builder
	jsJSONStringInto(&b, s)
	return b.String()
}

func jsJSONStringInto(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == 0x08:
			b.WriteString(`\b`)
		case c == 0x0c:
			b.WriteString(`\f`)
		case c < 0x20:
			fmt.Fprintf(b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// hasControlCharacter mirrors the /[\u0000-\u001f\u007f-\u009f]/u test.
func hasControlCharacter(s string) bool {
	for _, r := range s {
		if r <= 0x001f || (r >= 0x007f && r <= 0x009f) {
			return true
		}
	}
	return false
}

// jsTrimString mirrors ECMAScript String.prototype.trim: WhiteSpace
// (including U+00A0 and U+FEFF plus the Zs category) and LineTerminator
// characters are stripped from both ends.
func jsTrimString(s string) string {
	start := 0
	for start < len(s) {
		r, size := decodeJSRune(s[start:])
		if !jsIsSpace(r) {
			break
		}
		start += size
	}
	end := len(s)
	for end > start {
		r, size := decodeLastJSRune(s[:end])
		if !jsIsSpace(r) {
			break
		}
		end -= size
	}
	return s[start:end]
}

func jsIsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x0085, 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func decodeJSRune(s string) (rune, int) {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size <= 1 {
		return 0xfffd, 1
	}
	return r, size
}

func decodeLastJSRune(s string) (rune, int) {
	r, size := utf8.DecodeLastRuneInString(s)
	if r == utf8.RuneError && size <= 1 {
		return 0xfffd, 1
	}
	return r, size
}
