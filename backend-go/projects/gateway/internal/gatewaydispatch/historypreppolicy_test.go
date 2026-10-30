package gatewaydispatch

import (
	"testing"
)

// HistoryPrepPolicyForRequest 取值表测试（调度内核通用化批次 3b，
// docs/functions/调度内核通用化设计.md §5.4；xAI 供应商边界批按
// CodexOAuthVendorEligible 口径补 xAI Grok OAuth 形态）。
// 原 TestDefersCodexResponsesHistorySanitization 的四个场景已并入本表：
// defer 判定被策略计算取代，判定口径见 historypreppolicy.go 注释。
func TestHistoryPrepPolicyForRequest(t *testing.T) {
	apiKey := testAccounts("prep-policy-a")[0]
	oauthGPTShape := testAccounts("prep-policy-o")[0]
	oauthGPTShape.Type = "oauth"
	oauthGPTShape.VendorCode = "gpt"
	oauthGPTShape.VendorCode = "gpt"
	oauthGPTShape.ProtocolCode = "openai"
	oauthGPTShape.ProtocolVersion = "v1"
	oauthGPTShape.ProviderProtocolProfileID = GPTOpenAIV1ProfileID
	// B06 收口负例：空 vendor oauth 不再按 gpt，落到与非 oauth 一致的
	// 判定路径（见 CodexOAuthVendorEligible）。
	oauthEmptyVendorShape := oauthGPTShape
	oauthEmptyVendorShape.ID = "prep-policy-o-empty"
	oauthEmptyVendorShape.VendorCode = ""
	// 本批起为真实生产形态：xAI（Grok）OAuth 账户，协议码同为 openai。
	oauthXaiShape := oauthGPTShape
	oauthXaiShape.ID = "prep-policy-o-xai"
	oauthXaiShape.VendorCode = "xai"
	oauthXaiShape.VendorCode = "xai"
	oauthXaiShape.ProviderProtocolProfileID = "profile_xai_openai_v1"

	cases := []struct {
		name    string
		account AccountCandidate
		compat  string
		family  string
		want    HistoryPrepPolicy
	}{
		{
			name:    "SDK→API Key：compat 为空",
			account: apiKey, compat: "", family: "responses",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStagePreserve, Post: HistoryPrepStagePreserve, SkipIfProcessed: false},
		},
		{
			name:    "SDK→API Key：非 codex 兼容 + chat 族",
			account: apiKey, compat: "openai_standard", family: "chat",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStagePreserve, Post: HistoryPrepStagePreserve, SkipIfProcessed: false},
		},
		{
			name:    "Codex→API Key：codex_responses + responses 族",
			account: apiKey, compat: "codex_responses", family: "responses",
			want: HistoryPrepPolicy{Front: HistoryPrepStageSanitizeInline, Adapter: HistoryPrepStagePreserve, Post: HistoryPrepStageSanitize, SkipIfProcessed: true},
		},
		{
			name:    "Codex→OAuth：defer 条件命中",
			account: oauthGPTShape, compat: "codex_responses", family: "responses",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStageSanitize, Post: HistoryPrepStagePreserve, SkipIfProcessed: true},
		},
		{
			name:    "SDK→OAuth：OAuth 能力门拒绝组合（compat 非空非 codex_responses）防御归并 defer 同值",
			account: oauthGPTShape, compat: "openai_standard", family: "responses",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStageSanitize, Post: HistoryPrepStagePreserve, SkipIfProcessed: true},
		},
		{
			name:    "SDK→OAuth：compat 为空（能力门放行面）同归并，适配器 sanitize 保持",
			account: oauthGPTShape, compat: "", family: "chat",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStageSanitize, Post: HistoryPrepStagePreserve, SkipIfProcessed: true},
		},
		{
			name:    "Chat bridge 形态：codex_responses compat 但非 responses 族 → API Key 全 preserve",
			account: apiKey, compat: "codex_responses", family: "chat",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStagePreserve, Post: HistoryPrepStagePreserve, SkipIfProcessed: false},
		},
		{
			name:    "Chat bridge 形态：codex_responses compat 但非 responses 族 → OAuth 归并值（适配器 sanitize 保持）",
			account: oauthGPTShape, compat: "codex_responses", family: "chat",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStageSanitize, Post: HistoryPrepStagePreserve, SkipIfProcessed: true},
		},
		{
			name:    "空 vendor oauth 不再按 gpt，落到 Codex→API Key 行",
			account: oauthEmptyVendorShape, compat: "codex_responses", family: "responses",
			want: HistoryPrepPolicy{Front: HistoryPrepStageSanitizeInline, Adapter: HistoryPrepStagePreserve, Post: HistoryPrepStageSanitize, SkipIfProcessed: true},
		},
		{
			name:    "xAI Grok OAuth：codex_responses + responses 族落到与非 oauth 一致的 Codex→API Key 行（hook 生产恒 nil 为 no-op）",
			account: oauthXaiShape, compat: "codex_responses", family: "responses",
			want: HistoryPrepPolicy{Front: HistoryPrepStageSanitizeInline, Adapter: HistoryPrepStagePreserve, Post: HistoryPrepStageSanitize, SkipIfProcessed: true},
		},
		{
			name:    "xAI Grok OAuth：compat 为空落到与非 oauth 一致的全 preserve",
			account: oauthXaiShape, compat: "", family: "chat",
			want: HistoryPrepPolicy{Front: HistoryPrepStagePreserve, Adapter: HistoryPrepStagePreserve, Post: HistoryPrepStagePreserve, SkipIfProcessed: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HistoryPrepPolicyForRequest(tc.account, tc.compat, tc.family)
			if got != tc.want {
				t.Fatalf("policy = %+v, want %+v", got, tc.want)
			}
		})
	}
}
