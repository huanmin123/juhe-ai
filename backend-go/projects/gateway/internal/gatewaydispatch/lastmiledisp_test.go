package gatewaydispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 检查锁观测投影、分组回退繁忙跳过与模型映射运行时来源。
//（原非流式 Reader 管道族检查测试已随 BUG-0247 项 3 删除——生产管道在
// gatewayresponse 包实现。）

// stateLocks 返回非阻断的锁状态（观测投影）。
type stateLocks struct {
	blockingLocks
}

func (stateLocks) FindStateAsync(context.Context, string) (*AccountLockStateView, error) {
	return &AccountLockStateView{Generation: 5, IncidentID: "inc-1"}, nil
}

func TestDispatchRecordsAccountLockObservation(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Locks = stateLocks{}
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.Status != http.StatusBadGateway {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

// TestResolveNextGroupFallbackSkipsBusyCapacity: 容量繁忙原因下繁忙组被跳过。
func TestResolveNextGroupFallbackSkipsBusyCapacity(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	engine.Cache = &fakeCacheFallback{
		accounts: map[string][]AccountCandidate{
			"group-2": testAccounts("b-1"),
			"group-3": testAccounts("c-1"),
		},
	}
	// group-2 满载 → busy 跳过；group-3 正常。
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"b-1": 4, "c-1": 0}}
	apiKeyRecord := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "apikey-1",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
			{GroupID: "group-3", Status: "active", GroupEnabled: 1},
		},
	}
	candidate, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
		Req:             req,
		Reason:          "group_capacity_busy",
		APIKeyRecord:    apiKeyRecord,
		SystemAccountID: "system-1",
		GroupID:         "group-1",
		RequestLane:     "text",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !found || candidate.GroupID != "group-3" {
		t.Fatalf("candidate = %#v found=%v", candidate, found)
	}
}

// TestFilterGatewayAccountsByRequestedModelRuntimeMapping: 运行时来源映射
// 与端点族匹配。
func TestFilterGatewayAccountsByRequestedModelRuntimeMapping(t *testing.T) {
	runtimeSource := "route_rule"
	accounts := []AccountCandidate{{
		ID:              "mapped",
		SupportedModels: []string{"upstream-model"},
		ProviderCode:    "openai",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "gpt-test",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
			UpstreamModel:          "upstream-model",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
			RuntimeSource:          &runtimeSource,
		}},
	}}
	result := FilterGatewayAccountsByRequestedModel(accounts, "gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if result.MappingMatchedCount != 1 || result.ModelPriority.RankByAccountID["mapped"] != ModelPriorityRankMapping {
		t.Fatalf("result = %#v", result)
	}
	// 不支持的 input 类型 → invalidModelConstraint。
	invalid := []AccountCandidate{{ID: "no-constraint"}}
	result = FilterGatewayAccountsByRequestedModel(invalid, "gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if result.InvalidModelConstraintCount != 1 || result.Reason != "unsupported_model" {
		t.Fatalf("result = %#v", result)
	}
}
