package gatewayupstream

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
)

// Provider protocol tokens, migrated from domain/provider-protocol.ts
// (git HEAD). The two-line predicates are mirrored locally like the other
// gateway slices do (gatewayopenai owns the canonical constants).

const (
	// GPTVendorCode mirrors GPT_VENDOR_CODE.
	GPTVendorCode = "gpt"
	// GPTOpenAIV1ProfileID mirrors GPT_OPENAI_V1_PROFILE_ID.
	GPTOpenAIV1ProfileID = "profile_gpt_openai_v1"
)

func NormalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// IsGptVendorCode mirrors isGptVendorCode.
func IsGptVendorCode(value string) bool {
	return NormalizeProviderToken(value) == GPTVendorCode
}

// isOpenAIProtocolProfile mirrors isOpenAIProtocolProfile on the
// runtime-cache secret.
func IsOpenAIProtocolProfileWith(protocolCode, protocolVersion string) bool {
	return gatewayopenai.ProtocolCode == NormalizeProviderToken(protocolCode) &&
		gatewayopenai.ProtocolVersion == NormalizeProviderToken(protocolVersion)
}

func isOpenAIProtocolProfile(account UpstreamHeaderAccount) bool {
	return IsOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion)
}

// IsOpenAIProtocolProfileSecret mirrors the predicate on the full secret.
func IsOpenAIProtocolProfileSecret(protocolCode, protocolVersion string) bool {
	return IsOpenAIProtocolProfileWith(protocolCode, protocolVersion)
}
