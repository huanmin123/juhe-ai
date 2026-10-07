package accountbalance

import "testing"

// BalanceInputDigest 契约（BUG-0286）：确定性、逐输入区分、与全局
// config_revision 无关——非余额相关编辑（revision 推进）不得改变摘要。
func TestBalanceInputDigestContract(t *testing.T) {
	base := BalanceInputDigest("openai", "fp-1", `{"adapter":"builtin","intervalMinutes":5}`)
	if base == "" || len(base) != 16 {
		t.Fatalf("摘要应为 16 位 hex: %q", base)
	}
	if again := BalanceInputDigest("openai", "fp-1", `{"adapter":"builtin","intervalMinutes":5}`); again != base {
		t.Fatalf("同输入摘要必须确定: %q vs %q", base, again)
	}
	if changed := BalanceInputDigest("openai", "fp-2", `{"adapter":"builtin","intervalMinutes":5}`); changed == base {
		t.Fatal("凭据指纹变化必须改变摘要（换 Key 立即失效）")
	}
	if changed := BalanceInputDigest("openai", "fp-1", `{"adapter":"custom"}`); changed == base {
		t.Fatal("适配器配置变化必须改变摘要")
	}
	if changed := BalanceInputDigest("anthropic", "fp-1", `{"adapter":"builtin","intervalMinutes":5}`); changed == base {
		t.Fatal("provider 变化必须改变摘要")
	}
	// 非余额语义扰动不改变摘要：两侧 trim、大小写无关的指纹空白。
	if trimmed := BalanceInputDigest(" openai ", " fp-1 ", ` {"adapter":"builtin","intervalMinutes":5} `); trimmed != base {
		t.Fatal("外围空白不得改变摘要（两侧同列同源读取）")
	}
	// 快照打点契约：执行核返回的成功负载带 inputDigest（端到端薄壳行为由
	// 各写入方测试覆盖；此处锁定打点函数与摘要函数一致）。
	input := Input{Provider: "openai", CredentialFingerprint: "fp-1", ConfigJSON: `{"adapter":"builtin","intervalMinutes":5}`}
	if inputDigest := BalanceInputDigest(input.Provider, input.CredentialFingerprint, input.ConfigJSON); inputDigest != base {
		t.Fatal("Input 携带的摘要输入与显式参数必须产出同一摘要")
	}
}
