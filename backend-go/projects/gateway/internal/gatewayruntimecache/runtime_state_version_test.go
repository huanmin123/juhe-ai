package gatewayruntimecache

// B3/B4 运行态版本栅栏：runtime 主路径一次 MGET 同时比较凭据版本、账户状态
// 序号与 API Key 状态序号；任一变化即重载 PG 快照（复用既有 runtime 单飞）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRuntimeVersions struct {
	mu        sync.Mutex
	creds     map[string]int64
	state     map[string]int64
	apiKey    int64
	err       error
	calls     int
	lastKeyID string
}

func (f *fakeRuntimeVersions) RuntimeVersions(_ context.Context, credentialIDs, stateIDs []string, apiKeyID string) (map[string]int64, map[string]int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastKeyID = apiKeyID
	if f.err != nil {
		return nil, nil, 0, f.err
	}
	credentials := make(map[string]int64, len(credentialIDs))
	for _, id := range credentialIDs {
		credentials[id] = f.creds[id]
	}
	state := make(map[string]int64, len(stateIDs))
	for _, id := range stateIDs {
		state[id] = f.state[id]
	}
	return credentials, state, f.apiKey, nil
}

func (f *fakeRuntimeVersions) setAccountState(id string, version int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = map[string]int64{}
	}
	f.state[id] = version
}

func (f *fakeRuntimeVersions) setAPIKey(version int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiKey = version
}

func (f *fakeRuntimeVersions) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func TestRuntimeAccountStateVersionChangeReloads(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	groupAccess := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", GroupAccessType: GroupAccessTypeOwner}
	account := testOAuthAccount("a1", "sys_owner")
	models.runtimes["sk-state"] = staticRuntime(t, models, testAPIKeyRow("key_state", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{account})
	reader := &fakeRuntimeVersions{state: map[string]int64{"a1": 1}}
	svc := newTestService(t, models, newManualClock(), func(opts *Options) { opts.RuntimeVersions = reader })

	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-state"); err != nil {
		t.Fatalf("首读: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("首读 loader = %d, want 1", models.runtimeCallCount())
	}
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-state"); err != nil {
		t.Fatalf("命中读: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("状态未变不应重载, loader = %d", models.runtimeCallCount())
	}
	// jobs 写 temporary_unavailable / 关闸 → 状态序号变化 → 下一次读重载。
	reader.setAccountState("a1", 2)
	updated := testOAuthAccount("a1", "sys_owner")
	updated.Status = "temporary_unavailable"
	models.runtimes["sk-state"] = staticRuntime(t, models, testAPIKeyRow("key_state", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{updated})
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-state"); err != nil {
		t.Fatalf("状态变化后读: %v", err)
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("状态序号变化必须重载, loader = %d", models.runtimeCallCount())
	}
}

func TestRuntimeAPIKeyStateVersionChangeReloads(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	groupAccess := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", GroupAccessType: GroupAccessTypeOwner}
	models.runtimes["sk-key"] = staticRuntime(t, models, testAPIKeyRow("key_gate", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{testOAuthAccount("a1", "sys_owner")})
	reader := &fakeRuntimeVersions{apiKey: 1}
	svc := newTestService(t, models, newManualClock(), func(opts *Options) { opts.RuntimeVersions = reader })

	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-key"); err != nil {
		t.Fatalf("首读: %v", err)
	}
	reader.setAPIKey(2)
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-key"); err != nil {
		t.Fatalf("状态变化后读: %v", err)
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("API Key 状态序号变化必须重载, loader = %d", models.runtimeCallCount())
	}
}

func TestRuntimeStateVersionReadErrorKeepsCache(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	groupAccess := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", GroupAccessType: GroupAccessTypeOwner}
	models.runtimes["sk-state-err"] = staticRuntime(t, models, testAPIKeyRow("key_state_err", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{testOAuthAccount("a1", "sys_owner")})
	reader := &fakeRuntimeVersions{state: map[string]int64{"a1": 1}}
	svc := newTestService(t, models, newManualClock(), func(opts *Options) { opts.RuntimeVersions = reader })
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-state-err"); err != nil {
		t.Fatalf("首读: %v", err)
	}
	reader.setErr(errors.New("redis down"))
	time.Sleep(time.Millisecond)
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-state-err"); err != nil {
		t.Fatalf("版本读错误不得阻断: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("版本读错误不应重载, loader = %d", models.runtimeCallCount())
	}
}
