package chat

// w10d 覆盖收尾：web_search 与提示缓存模型的完整流式链路。

import (
	"net/http"
	"strings"
	"testing"
)

// w10dWebSearchCatalog 在 mockModelCatalog 基础上为 gpt-5 增加 web_search 与提示缓存。
type w10dWebSearchCatalog struct{}

func (w10dWebSearchCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	return (mockModelCatalog{}).ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily)
}

func (w10dWebSearchCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	items := (mockModelCatalog{}).ListProviderCatalog(providerCode, systemAccountID)
	for i := range items {
		if items[i].Model == "gpt-5" {
			items[i].SupportedTools = append(items[i].SupportedTools, "web_search")
			promptCaching := true
			items[i].SupportsPromptCaching = &promptCaching
		}
	}
	return items
}

// TestW10DStreamWebSearchPromptCaching 驱动 web_search + 提示缓存分支与完整流式链路。
func TestW10DStreamWebSearchPromptCaching(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = w10dWebSearchCatalog{}
	env.fixture.createConversation("conv_w10d_ws", routeTestOwner)
	env.executor.steps = []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
		return sseResponse(responsesTextSSE("搜索完成"))
	}}}
	response := env.streamPost("conv_w10d_ws", routeTestOwner, streamPayload("ws-1", "查一下天气", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("web_search 流式 = %d %s", response.status, response.rawString())
	}
	if !strings.Contains(response.rawString(), "搜索完成") {
		t.Fatalf("缺少回答: %s", response.rawString())
	}
}
