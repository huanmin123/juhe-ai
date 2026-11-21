package accountstransfer

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// 批量编辑的健康检查形态必须与 accountscore 写侧白名单同源：2026-11-20
// 向量/重排批新增 embeddings_json/rerank_json，且既有 audio/video/realtime
// 形态也不能因批量通道使用旧 11 值表而 400。测试直接遍历 canonical 27 值
// 集合，防止后续把白名单缩回旧子集。
func TestValidateBatchUpdateHealthCheckEndpointModesUseCanonicalWhitelist(t *testing.T) {
	if len(accountscore.AccountHealthCheckEndpointModes) != 27 {
		t.Fatalf("canonical health check mode count = %d, want 27", len(accountscore.AccountHealthCheckEndpointModes))
	}
	for mode := range accountscore.AccountHealthCheckEndpointModes {
		t.Run(mode, func(t *testing.T) {
			if got := ValidateBatchUpdateValue("healthCheckEndpointMode", mode); got != "" {
				t.Fatalf("ValidateBatchUpdateValue(%q) = %q, want accepted", mode, got)
			}
		})
	}
	if got := ValidateBatchUpdateValue("healthCheckEndpointMode", "bogus_mode"); got != BatchFieldsPrompt {
		t.Fatalf("invalid mode validation = %q, want %q", got, BatchFieldsPrompt)
	}
	if got := ValidateBatchUpdateValue("healthCheckEndpointMode", 42); got != BatchFieldsPrompt {
		t.Fatalf("non-string mode validation = %q, want %q", got, BatchFieldsPrompt)
	}
}

func TestValidateBatchUpdateSupportedEndpointModesAllowsFullVocabulary(t *testing.T) {
	modes := make([]any, 0, len(accountscore.AccountHealthCheckEndpointModes))
	for mode := range accountscore.AccountHealthCheckEndpointModes {
		modes = append(modes, mode)
	}
	if got := ValidateBatchUpdateValue("supportedEndpointModes", modes); got != "" {
		t.Fatalf("27 valid modes validation = %q, want accepted", got)
	}

	tooMany := make([]any, 0, 28)
	for index := 0; index < 28; index++ {
		tooMany = append(tooMany, "chat_json")
	}
	if got := ValidateBatchUpdateValue("supportedEndpointModes", tooMany); got != BatchFieldsPrompt {
		t.Fatalf("28 entries validation = %q, want %q", got, BatchFieldsPrompt)
	}
}
