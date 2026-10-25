package accounts

// w14l 覆盖率收尾（三）：reserveAndEnqueueAccountHealthSnapshot 版本臂、
// groupOwnerAndProvider 空属主臂、测试会话跨域/缺失臂与取消原因传播臂。

import (
	"strings"
	"testing"
)

func TestW14LMiscPureArms(t *testing.T) {
	// 单字符掩码臂。
	if got := MaskSecret("x"); got != "x***" {
		t.Fatalf("单字符掩码不符：%q", got)
	}
	// 内置适配器携带 custom 配置 → 拒绝臂。
	if _, err := NormalizeAccountBalanceConfig(map[string]any{
		"adapter": "builtin", "custom": map[string]any{},
	}); err == nil {
		t.Fatal("内置查询不应接受 custom 配置")
	}
	// 账户名搜索 3-gram。
	terms := accountNameSearchQueryTerms("w14l")
	if len(terms) == 0 {
		t.Fatal("应产出搜索词")
	}
	if got := accountNameSearchQueryTerms(""); got != nil {
		t.Fatalf("空关键词应返回 nil：%v", got)
	}
}

func TestW14LMiscPureArms2(t *testing.T) {
	// 额度恢复策略：超长策略拒绝臂（两个账户类型都带超长时区文本撑大编码长度）。
	huge := strings.Repeat("w", quotaRecoveryMaxPolicyBytes)
	if _, err := normalizeQuotaRecoveryPolicy(map[string]any{
		"api_key":      map[string]any{"timezone": huge},
		"google_oauth": map[string]any{"timezone": huge},
	}); err == nil {
		t.Fatal("超长策略应拒绝")
	}
	// 客户端兼容：codex 响应默认值与非法值（gpt + openai/v1 才走严格校验）。
	profile := protocolProfileRef{ProviderCode: "gpt", ProtocolCode: "openai", ProtocolVersion: "v1"}
	compat, err := normalizeOpenAIAccountClientCompatibility("gpt", "api_key", "", profile)
	if err != nil || compat == "" {
		t.Fatalf("空值应有默认兼容形态：%q %v", compat, err)
	}
	if _, err := normalizeOpenAIAccountClientCompatibility("gpt", "api_key", "bogus-compat", profile); err == nil {
		t.Fatal("非法兼容值应拒绝")
	}
	// 数字判定臂。
	if !isAllDigits("204") || isAllDigits("") || isAllDigits("20a") {
		t.Fatal("isAllDigits 判定不符")
	}
}
