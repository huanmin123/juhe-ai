package main

// w1hReadModels：可编程的 gatewayruntimecache.ReadModels 测试双（原定义于
// w1_chat_family_test.go；my-chat 会话面退役后该 fake 仍是 runtime cache 端
// 口测试的通用夹具，迁移至本文件独立存活）。目录投影按 provider/system
// 账户过滤，其余端口恒返回零值/空集。

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

type w1hReadModels struct {
	groupAccess *gatewayruntimecache.GroupUsageAccessMetadata
	accounts    []gatewayruntimecache.OpenAIAccountSecret
	catalog     []gatewayruntimecache.ProviderModelCatalogItem
}

func (m *w1hReadModels) ReadGatewaySettings(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, nil
}

func (m *w1hReadModels) ReadGatewayRuntime(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}

func (m *w1hReadModels) ResolveGroupUsageAccessMetadata(context.Context, string, string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	return m.groupAccess, nil
}

func (m *w1hReadModels) ListOpenAIAccountsForGroupResult(context.Context, string, string, gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: m.accounts}, nil
}

func (m *w1hReadModels) ListActiveResponseInspectionPolicies(context.Context, string, string) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return nil, nil
}

func (m *w1hReadModels) ListProviderModelCatalog(_ context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	out := []gatewayruntimecache.ProviderModelCatalogItem{}
	for _, item := range m.catalog {
		if input.ProviderCode != "" && item.ProviderCode != input.ProviderCode {
			continue
		}
		if input.SystemAccountID != "" && item.SystemAccountID != nil && *item.SystemAccountID != input.SystemAccountID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (m *w1hReadModels) LoadAccountCurrentConcurrencyByID(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

// ReadGatewayRuntimeByAPIKeyID 是 M5b realtime 按 id 读（接口成员）：测试
// 双不消费该面，恒返回空运行时。
func (m *w1hReadModels) ReadGatewayRuntimeByAPIKeyID(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}
