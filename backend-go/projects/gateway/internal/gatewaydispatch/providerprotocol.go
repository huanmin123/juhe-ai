package gatewaydispatch

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

func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// IsGptVendorCode mirrors isGptVendorCode.
func IsGptVendorCode(value string) bool {
	return normalizeProviderToken(value) == GPTVendorCode
}

// isOpenAIProtocolProfile mirrors isOpenAIProtocolProfile on the
// runtime-cache secret.
func isOpenAIProtocolProfileWith(protocolCode, protocolVersion string) bool {
	return gatewayopenai.ProtocolCode == normalizeProviderToken(protocolCode) &&
		gatewayopenai.ProtocolVersion == normalizeProviderToken(protocolVersion)
}

func isOpenAIProtocolProfile(account UpstreamHeaderAccount) bool {
	return isOpenAIProtocolProfileWith(account.ProtocolCode, account.ProtocolVersion)
}

// isOpenAIProtocolProfileSecret mirrors the predicate on the full secret.
func isOpenAIProtocolProfileSecret(protocolCode, protocolVersion string) bool {
	return isOpenAIProtocolProfileWith(protocolCode, protocolVersion)
}
