package accountbalance

import (
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// BalanceInputDigest 契约（BUG-0286）：确定性、逐输入区分、与全局
// config_revision 无关——非余额相关编辑（revision 推进）不得改变摘要。
func TestBalanceInputDigestContract(t *testing.T) {
	identity := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://api.openai.com",
		"api_key":  "sk-test",
	})
	base := BalanceInputDigest("openai", "fp-1", `{"adapter":"builtin","intervalMinutes":5}`, identity, "")
	if base == "" || len(base) != 16 {
		t.Fatalf("摘要应为 16 位 hex: %q", base)
	}
	if again := BalanceInputDigest("openai", "fp-1", `{"adapter":"builtin","intervalMinutes":5}`, identity, ""); again != base {
		t.Fatalf("同输入摘要必须确定: %q vs %q", base, again)
	}
	if changed := BalanceInputDigest("openai", "fp-2", `{"adapter":"builtin","intervalMinutes":5}`, identity, ""); changed == base {
		t.Fatal("凭据指纹变化必须改变摘要（换主 Key 立即失效）")
	}
	if changed := BalanceInputDigest("openai", "fp-1", `{"adapter":"custom"}`, identity, ""); changed == base {
		t.Fatal("适配器配置变化必须改变摘要")
	}
	if changed := BalanceInputDigest("anthropic", "fp-1", `{"adapter":"builtin","intervalMinutes":5}`, identity, ""); changed == base {
		t.Fatal("provider 变化必须改变摘要")
	}
	if changed := BalanceInputDigest("openai", "fp-1", `{"adapter":"builtin","intervalMinutes":5}`, identity, "proxy-1"); changed == base {
		t.Fatal("换绑代理必须改变摘要")
	}
	// 非余额语义扰动不改变摘要：两侧 trim（同一列值读出，仅外围空白差异；
	// 纯空白 proxy trims 为空串与 base 相同）。
	if trimmed := BalanceInputDigest(" openai ", " fp-1 ", ` {"adapter":"builtin","intervalMinutes":5} `, " "+identity+" ", "   "); trimmed != base {
		t.Fatal("外围空白不得改变摘要（两侧同列同源读取）")
	}
	// 快照打点契约：执行核返回的成功负载带 inputDigest（端到端薄壳行为由
	// 各写入方测试覆盖；此处锁定打点输入与摘要函数一致）。
	input := Input{Provider: "openai", CredentialFingerprint: "fp-1", ConfigJSON: `{"adapter":"builtin","intervalMinutes":5}`, ProxyProfileID: ""}
	if inputDigest := BalanceInputDigest(input.Provider, input.CredentialFingerprint, input.ConfigJSON, identity, input.ProxyProfileID); inputDigest != base {
		t.Fatal("Input 携带的摘要输入与显式参数必须产出同一摘要")
	}
}

// CredentialBalanceIdentity 契约：base_url 与有效 Key 全池任一变化必须改变
// 身份；主 Key 指纹（credential_fingerprint）覆盖不到的池内次成员与端点
// 编辑由该身份补齐（审核轮）。
func TestCredentialBalanceIdentityContract(t *testing.T) {
	base := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://api.openai.com",
		"api_keys": []any{"sk-a", "sk-b"},
	})
	if base == "" || len(base) != 16 {
		t.Fatalf("身份应为 16 位 hex: %q", base)
	}
	if again := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://api.openai.com",
		"api_keys": []any{"sk-a", "sk-b"},
	}); again != base {
		t.Fatal("同凭据身份必须确定")
	}
	if changed := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://api.openai.com",
		"api_keys": []any{"sk-a", "sk-c"},
	}); changed == base {
		t.Fatal("Key 池次成员变化必须改变身份（fingerprint 覆盖不到的盲区）")
	}
	if changed := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://relay.example.com",
		"api_keys": []any{"sk-a", "sk-b"},
	}); changed == base {
		t.Fatal("base_url 变化必须改变身份（余额端点直接由它构造）")
	}
	if trimmed := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://api.openai.com",
		"api_keys": []any{" sk-a ", "sk-b"},
	}); trimmed != base {
		t.Fatal("Key 外围空白不得改变身份（EffectiveAPIKeys 提取语义两侧一致）")
	}
	if changed := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://api.openai.com/",
		"api_keys": []any{"sk-a", "sk-b"},
	}); changed == base {
		t.Fatal("base_url 尾斜杠必须改变身份（端点路径归一不做原文改写）")
	}
	if changed := CredentialBalanceIdentity(map[string]any{
		"base_url": "https://api.openai.com",
		"api_keys": []any{"sk-a", "sk-b", "sk-c"},
	}); changed == base {
		t.Fatal("Key 池追加成员必须改变身份")
	}
}

// TestBalanceInputDigestDecryptParity 锁定读端摘要现算的解密前提：执行核
// 打点侧（openCredential → DecryptV1Envelope）与 gateway 读端闭包
// （accountcrypto.DecryptJSON）是两份 v1 envelope 解密实现，必须在同一
// credentials_encrypted 密文上产出等价 payload 与相同摘要——否则同一账户
// 两侧各算各的，显示判据永久失配（全部待查询）。
func TestBalanceInputDigestDecryptParity(t *testing.T) {
	const secret = "parity-secret"
	envelope, err := NewCredentialEnvelope(secret, "api_key", map[string]any{
		"api_key":  "sk-parity",
		"base_url": "https://parity.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	writerPayload := map[string]any{}
	if err := openCredential(secret, envelope, "api_key", &writerPayload); err != nil {
		t.Fatalf("执行核解密失败: %v", err)
	}
	readerPayload := map[string]any{}
	if err := accountcrypto.DecryptJSON(secret, envelope.Ciphertext, &readerPayload); err != nil {
		t.Fatalf("读端解密失败: %v", err)
	}
	if !reflect.DeepEqual(writerPayload, readerPayload) {
		t.Fatalf("两套解密实现 payload 不等价: %#v vs %#v", writerPayload, readerPayload)
	}
	writerDigest := BalanceInputDigest("openai", "fp-1", `{"adapter":"builtin"}`, CredentialBalanceIdentity(writerPayload), "proxy-1")
	readerDigest := BalanceInputDigest("openai", "fp-1", `{"adapter":"builtin"}`, CredentialBalanceIdentity(readerPayload), "proxy-1")
	if writerDigest != readerDigest {
		t.Fatalf("两侧摘要必须一致: %q vs %q", writerDigest, readerDigest)
	}
}
