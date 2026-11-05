package gatewaydispatch

import "testing"

// T36（2026-11-05 退役后）：generic_http 不再是连接器，空 vendor 账户不得
// 进入 API Key 池隔离；真实厂商白名单保持不变。
func TestIsAccountAPIKeyPoolProviderSupportedRetiredGeneric(t *testing.T) {
	if isAccountAPIKeyPoolProviderSupported("", "generic_http", "openai", "v1") {
		t.Fatal("generic_http 已退役，不得支持 API Key 池隔离")
	}
	if isAccountAPIKeyPoolProviderSupported("", "", "openai", "v1") {
		t.Fatal("空 driver + 空 vendor 不得支持")
	}
	if !isAccountAPIKeyPoolProviderSupported("gpt", "", "openai", "v1") {
		t.Fatal("gpt vendor 应保持支持")
	}
}
