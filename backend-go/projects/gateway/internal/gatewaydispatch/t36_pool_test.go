package gatewaydispatch

import "testing"

// T36：generic_http 是连接器，不是供应商；空 vendor 不得作为通配符，但
// connection_driver_code=generic_http 的 API-key 账户必须保留池隔离。
func TestIsAccountAPIKeyPoolProviderSupportedGenericHTTP(t *testing.T) {
	if !isAccountAPIKeyPoolProviderSupported("", "generic_http", "openai", "v1") {
		t.Fatal("generic_http 应支持 API Key 池隔离")
	}
	if isAccountAPIKeyPoolProviderSupported("", "", "openai", "v1") {
		t.Fatal("空 driver + 空 vendor 不得支持")
	}
	if !isAccountAPIKeyPoolProviderSupported("gpt", "", "openai", "v1") {
		t.Fatal("gpt vendor 应保持支持")
	}
}
