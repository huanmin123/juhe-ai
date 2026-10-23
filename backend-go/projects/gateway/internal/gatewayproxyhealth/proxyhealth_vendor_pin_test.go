package gatewayproxyhealth_test

// 供应商判据防漂移钉：gatewayproxyhealth 被 gatewaydispatch 反向依赖
//（capacity.go / circuitfacade.go / upstreamdispatch.go），生产代码无法直接
// import gatewaydispatch.CodexOAuthVendorEligible（编译环），桶键侧使用镜像
// 谓词 codexOAuthVendorBucketEligible。本文件位于外部测试包，可无环 import
// gatewaydispatch，把镜像谓词的可见行为（oauth 账户桶键是否固定 ChatGPT
// Codex 常量）逐值钉到权威判据上——两侧任一方漂移都会在此失败。
// 判据收口（vendor_code 回填迁移完成后空值不再合法）时同步扩展本表。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestGatewayBaseURLBucketKeyPinnedToCodexOAuthVendorEligible(t *testing.T) {
	// 覆盖合法值、迁移期空值、大小写/空白（权威判据为原样比较，不得归一化）
	// 与已知非 gpt 厂商。
	vendors := []string{"gpt", "", "xai", "GPT", " gpt", "anthropic", "gemini", "deepseek"}
	for _, vendor := range vendors {
		account := gatewayruntimecache.OpenAIAccountSecret{
			ID:              "acc_pin",
			SystemAccountID: "sys_pin",
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			VendorCode:      vendor,
			Type:            "oauth",
			BaseURL:         "https://cli-chat-proxy.grok.com/v1",
		}
		keys := gatewayproxyhealth.GatewayUpstreamBucketKeys(account, gatewayproxyhealth.BucketScopeUpstream)
		wantPinned := gatewaydispatch.CodexOAuthVendorEligible(vendor)
		gotPinned := false
		for _, key := range keys {
			if key == "baseUrl:https://chatgpt.com/backend-api/codex:owner:sys_pin" {
				gotPinned = true
			}
		}
		if gotPinned != wantPinned {
			t.Fatalf("vendor %q: chatgpt codex 常量桶 = %v, want %v（桶键供应商判据必须与 gatewaydispatch.CodexOAuthVendorEligible 逐值一致）", vendor, gotPinned, wantPinned)
		}
		if !gotPinned {
			for _, key := range keys {
				if key == "baseUrl:https://cli-chat-proxy.grok.com/v1:owner:sys_pin" {
					gotPinned = true
				}
			}
			if !gotPinned {
				t.Fatalf("vendor %q: 非 gpt vendor 必须按真实 BaseURL 入桶，keys = %v", vendor, keys)
			}
		}
	}
}
