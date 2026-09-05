// Client-compatibility projection: the full port of Node
// normalizeOpenAIAccountClientCompatibility
// (backend/src/domain/account-client-compatibility.ts:30-49) for the read
// surfaces that render a stored clientCompatibility value. It replaces the
// literal-only shortcut for the account list/detail projections; the clone
// context keeps its pass-through rendering because the Node counterpart
// (account-interaction-context.repository.ts:399) also passes the stored
// value through without this normalization.
package accounts

import (
	"errors"
	"strings"
)

// Protocol tokens mirror provider-protocol.ts:1-2; the gpt vendor token is
// the package-wide gptVendorCode constant (import_source.go), matching
// provider-protocol.ts:4.
const (
	openAIProtocolCode    = "openai"
	openAIProtocolVersion = "v1"
)

// protocolProfileRef mirrors the protocolProfile argument Node passes from
// the management list (account-management-list.repository.ts:522). Only
// protocolCode and protocolVersion participate in the openai/v1 predicate
// (provider-protocol.ts:60-63); providerCode and providerProtocolProfileID
// ride along in the Node object unused, so the predicate ignores them here.
type protocolProfileRef struct {
	ProviderCode              string
	ProtocolCode              string
	ProtocolVersion           string
	ProviderProtocolProfileID string
}

// normalizeProviderToken mirrors provider-protocol.ts:113-117 (trim +
// lowercase; Node maps the empty result to undefined, which never equals the
// tokens below — the empty Go string compares the same way).
func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// deriveOpenAIAccountClientCompatibility mirrors
// deriveOpenAIAccountClientCompatibility (account-client-compatibility.ts:50-66):
// the stored-value-free derivation used when writing a new account — an
// openai/v1 profile renders codex_responses for gpt vendor accounts of the
// api_key/oauth types and openai_standard otherwise.
func deriveOpenAIAccountClientCompatibility(providerCode, accountType string, profile protocolProfileRef) string {
	if !isOpenAIProtocolProfileOf(protocolPredicateInput{
		providerCode:              profile.ProviderCode,
		protocolCode:              profile.ProtocolCode,
		protocolVersion:           profile.ProtocolVersion,
		providerProtocolProfileID: profile.ProviderProtocolProfileID,
	}) {
		return "openai_standard"
	}
	if isGptVendorCodeToken(providerCode) && (accountType == "oauth" || accountType == "api_key") {
		return "codex_responses"
	}
	return "openai_standard"
}

// normalizeOpenAIAccountClientCompatibility renders the effective client
// compatibility for one account row (account-client-compatibility.ts:30-49):
// a gpt vendor account on an openai/v1 protocol profile renders
// codex_responses for the oauth type unconditionally and otherwise passes the
// stored value through the strict check — NULL/empty collapses to
// codex_responses, openai_standard/codex_responses render as stored, and any
// other stored value raises Node's 客户端兼容配置无效 error
// (normalizeAccountClientCompatibility :22-28). Every other vendor or
// protocol renders openai_standard, so a stored codex_responses on a non-gpt
// provider never leaks into the projection.
//
// Node's fourth `fallback` parameter is declared but never referenced inside
// the function body (:30-49 — the gpt branch pins 'codex_responses', the
// remaining branch pins 'openai_standard'), so the Go port omits the dead
// parameter; callers pass an empty value for SQL NULL, which behaves
// identically to Node's null under the strict check.
func normalizeOpenAIAccountClientCompatibility(providerCode, accountType, value string, profile protocolProfileRef) (string, error) {
	if normalizeProviderToken(providerCode) == gptVendorCode &&
		normalizeProviderToken(profile.ProtocolCode) == openAIProtocolCode &&
		normalizeProviderToken(profile.ProtocolVersion) == openAIProtocolVersion {
		if accountType == "oauth" {
			return "codex_responses", nil
		}
		switch value {
		case "":
			return "codex_responses", nil
		case "openai_standard", "codex_responses":
			return value, nil
		default:
			return "", errors.New("客户端兼容配置无效")
		}
	}
	return "openai_standard", nil
}
