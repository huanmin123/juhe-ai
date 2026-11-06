package gatewayruntimecache

// B2（跨进程共享状态改造）账户凭据版本投影的读取面测试：
//   - jobs 刷新后 bump 版本 → gateway 下一次读同步重载（不等 60s revalidate）；
//   - 只重载包含变化账户的组条目；
//   - 版本读失败/条目写失败回退既有缓存窗口，不阻断请求。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// testOAuthAccount 返回会参与凭据版本投影的 OAuth 账户（api_key 账户没有版本键）。
func testOAuthAccount(id string, groupOwner string) OpenAIAccountSecret {
	account := testAccount(id, groupOwner)
	account.Type = "oauth"
	return account
}

type fakeCredentialVersions struct {
	mu       sync.Mutex
	versions map[string]int64
	err      error
	calls    int
	lastIDs  []string
	// block 为 true 时 CredentialVersions 等到 ctx 结束（模拟 Redis 变慢）。
	block bool
}

func (f *fakeCredentialVersions) CredentialVersions(ctx context.Context, accountIDs []string) (map[string]int64, error) {
	f.mu.Lock()
	f.calls++
	f.lastIDs = append([]string(nil), accountIDs...)
	block := f.block
	err := f.err
	versions := make(map[string]int64, len(accountIDs))
	for _, id := range accountIDs {
		versions[id] = f.versions[id]
	}
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return versions, nil
}

func (f *fakeCredentialVersions) set(accountID string, version int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.versions == nil {
		f.versions = map[string]int64{}
	}
	f.versions[accountID] = version
}

func (f *fakeCredentialVersions) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCredentialVersions) callsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestCredentialVersionChangeReloadsOnNextRead(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 1}}
	svc := newTestService(t, counter, clock, func(opts *Options) { opts.CredentialVersions = reader })

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("首载 loader = %d, want 1", counter.accountCalls)
	}
	// 版本未变：窗口内命中不重载。
	clock.Advance(10 * time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("版本未变不应重载，loader = %d", counter.accountCalls)
	}

	// jobs 侧刷新提交：bump 版本 + DB 新凭据。下一次读必须同步重载。
	reader.set("a1", 2)
	updated := testOAuthAccount("a1", "sys")
	updated.APIKey = "rotated-secret"
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{updated}}
	clock.Advance(time.Second)
	accounts, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("版本变化后读取: %v", err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("版本变化必须同步重载，loader = %d", counter.accountCalls)
	}
	if len(accounts) != 1 || accounts[0].APIKey != "rotated-secret" {
		t.Fatalf("重载后快照 = %+v, want 新凭据", accounts)
	}
}

func TestCredentialVersionOnlyReloadsAffectedGroup(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	models.accounts["g2"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a2", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 1, "a2": 1}}
	svc := newTestService(t, counter, clock, func(opts *Options) { opts.CredentialVersions = reader })

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g2", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("首载 loader = %d, want 2", counter.accountCalls)
	}
	// 只有 a1（g1）变化。
	reader.set("a1", 2)
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g2", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("未受影响组不应重载，loader = %d", counter.accountCalls)
	}
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 3 {
		t.Fatalf("受影响组必须重载，loader = %d", counter.accountCalls)
	}
}

func TestCredentialVersionReadErrorKeepsCache(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 1}}
	svc := newTestService(t, counter, clock, func(opts *Options) { opts.CredentialVersions = reader })

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	reader.setErr(errors.New("redis down"))
	clock.Advance(time.Second)
	accounts, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(accounts) != 1 {
		t.Fatalf("版本读错误应回退缓存 = %v err=%v", accounts, err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("版本读错误不应重载，loader = %d", counter.accountCalls)
	}
}

func TestCredentialVersionReloadFailureKeepsLastGood(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	failing := &w11dFailingAccounts{fakeModels: models, failAfter: 1}
	clock := newManualClock()
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 1}}
	svc := newTestService(t, failing, clock, func(opts *Options) { opts.CredentialVersions = reader })

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	reader.set("a1", 2)
	clock.Advance(time.Second)
	accounts, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(accounts) != 1 || accounts[0].ID != "a1" {
		t.Fatalf("重载失败必须保留 last-good = %v err=%v", accounts, err)
	}
}

func TestCredentialVersionReadTimeoutFallsBack(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	reader := &fakeCredentialVersions{block: true}
	svc := newTestService(t, counter, clock, func(opts *Options) { opts.CredentialVersions = reader })

	started := time.Now()
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatalf("版本读超时不得阻断首载: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("版本读未受 50ms 预算约束，耗时 %v", elapsed)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("首载 loader = %d, want 1", counter.accountCalls)
	}
	// 版本未知条目：后续读不再触发版本变化重载（仍按 60s 窗口）。
	clock.Advance(time.Second)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("版本未知不应重载，loader = %d", counter.accountCalls)
	}
}

func TestCredentialVersionReaderSkippedWhenAccountsCacheDisabled(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 1}}
	svc := newTestService(t, counter, newManualClock(), func(opts *Options) {
		// 共享缓存模式：账户进程缓存恒禁用，每次读走 loader 现读 PG。
		opts.Shared = newFakeSharedFactory()
		opts.CredentialVersions = reader
	})
	for index := 0; index < 2; index++ {
		if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if counter.accountCalls != 2 {
		t.Fatalf("共享模式应每次现读，loader = %d, want 2", counter.accountCalls)
	}
	if calls := reader.callsCount(); calls != 0 {
		t.Fatalf("账户缓存禁用时不应读版本键，reader calls = %d", calls)
	}
}

func TestCredentialVersionReaderNotConfiguredKeepsWindow(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testOAuthAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	svc := newTestService(t, counter, newManualClock(), nil)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("未配置版本端口应保持既有窗口，loader = %d", counter.accountCalls)
	}
}

// TestRuntimeCredentialVersionChangeReloadsOnNextRead：主派发路径（runtime
// cache）在 jobs 轮换 bump 版本后，下一次读同步重载并拿到新凭据，而不是等
// 60s revalidate。这条路径是 /v1 preflight 实际使用的 runtime.Accounts。
func TestRuntimeCredentialVersionChangeReloadsOnNextRead(t *testing.T) {
	models := newFakeModels()
	groupAccess := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", GroupAccessType: GroupAccessTypeOwner}
	models.runtimes["sk-version"] = staticRuntime(t, models, testAPIKeyRow("key_version", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{testOAuthAccount("a1", "sys_owner")})
	clock := newManualClock()
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 2}}
	svc := newTestService(t, models, clock, func(opts *Options) { opts.CredentialVersions = reader })
	ctx := context.Background()

	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-version"); err != nil {
		t.Fatalf("首读: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("首读 loader = %d, want 1", models.runtimeCallCount())
	}
	// 版本未变：窗口内命中不重载。
	clock.Advance(10 * time.Second)
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-version"); err != nil {
		t.Fatalf("命中读: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("版本未变不应重载, loader = %d", models.runtimeCallCount())
	}

	// jobs 轮换：bump 版本 + 更新运行时数据。下一次读必须同步重载。
	reader.set("a1", 3)
	updated := testOAuthAccount("a1", "sys_owner")
	updated.APIKey = "rotated-secret"
	models.runtimes["sk-version"] = staticRuntime(t, models, testAPIKeyRow("key_version", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{updated})
	clock.Advance(time.Second)
	runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-version")
	if err != nil {
		t.Fatalf("版本变化后读: %v", err)
	}
	if models.runtimeCallCount() != 2 {
		t.Fatalf("版本变化必须同步重载, loader = %d", models.runtimeCallCount())
	}
	if len(runtime.Accounts) != 1 || runtime.Accounts[0].APIKey != "rotated-secret" {
		t.Fatalf("重载后 runtime.Accounts = %+v, want 新凭据", runtime.Accounts)
	}
}

// TestRuntimeCredentialVersionReadErrorKeepsCache：版本读取失败时主路径继续
// 使用既有 runtime 快照（不阻断请求）。
func TestRuntimeCredentialVersionReadErrorKeepsCache(t *testing.T) {
	models := newFakeModels()
	groupAccess := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", GroupAccessType: GroupAccessTypeOwner}
	models.runtimes["sk-version-err"] = staticRuntime(t, models, testAPIKeyRow("key_version_err", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{testOAuthAccount("a1", "sys_owner")})
	clock := newManualClock()
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 2}}
	svc := newTestService(t, models, clock, func(opts *Options) { opts.CredentialVersions = reader })
	ctx := context.Background()
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-version-err"); err != nil {
		t.Fatalf("首读: %v", err)
	}
	reader.setErr(errors.New("redis down"))
	clock.Advance(time.Second)
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-version-err"); err != nil {
		t.Fatalf("版本读错误不得阻断: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("版本读错误不应重载, loader = %d", models.runtimeCallCount())
	}
}

// TestCredentialVersionSnapshotAheadRetriesLoad：版本号与 config_revision 同值，
// 快照版本领先说明“读数据早于最近一次提交”，必须重读一次；仍领先则在丢弃
// 版本快照后照常缓存（退回 60s 窗口），避免 PG 回滚等永久领先场景退化为
// 每请求重装载。
func TestCredentialVersionSnapshotAheadRetriesLoad(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	account := testOAuthAccount("a1", "sys")
	revision := int64(1)
	account.ConfigRevision = &revision
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{account}}
	counter := &whCountingModels{fakeModels: models}
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 5}}
	svc := newTestService(t, counter, newManualClock(), func(opts *Options) { opts.CredentialVersions = reader })

	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatalf("读: %v", err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("版本领先应重读一次, loader = %d, want 2", counter.accountCalls)
	}
	// 二次仍领先：条目丢弃版本快照后缓存，后续读按 60s 窗口命中。
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatalf("再次读: %v", err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("未收敛条目应退回缓存窗口, loader = %d, want 2", counter.accountCalls)
	}
}

// TestRuntimeCredentialSnapshotAheadFallsBackToWindow：runtime 装载遇到版本
// 领先（PG 回滚/旧快照导入等）时丢弃版本快照并照常缓存，后续读按 60s 窗口
// 命中，不退化为每请求重装载。
func TestRuntimeCredentialSnapshotAheadFallsBackToWindow(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	groupAccess := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner", GroupAccessType: GroupAccessTypeOwner}
	account := testOAuthAccount("a1", "sys_owner")
	revision := int64(1)
	account.ConfigRevision = &revision
	models.runtimes["sk-ahead"] = staticRuntime(t, models, testAPIKeyRow("key_ahead", "normal", "g1"),
		groupAccess, []OpenAIAccountSecret{account})
	reader := &fakeCredentialVersions{versions: map[string]int64{"a1": 5}}
	svc := newTestService(t, models, newManualClock(), func(opts *Options) { opts.CredentialVersions = reader })

	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-ahead"); err != nil {
		t.Fatalf("首读: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("首读 loader = %d, want 1", models.runtimeCallCount())
	}
	// 条目已缓存（版本快照被丢弃）：60s 窗口内不重载。
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-ahead"); err != nil {
		t.Fatalf("命中读: %v", err)
	}
	if models.runtimeCallCount() != 1 {
		t.Fatalf("领先快照应退回缓存窗口, loader = %d, want 1", models.runtimeCallCount())
	}
}

// TestCredentialVersionsUseSourceAccountIDAndSkipAPIKey：授权实例账户的凭据
// 来自源账户，版本键必须用 source ID；api_key 账户没有版本键，不应进入 MGET。
func TestCredentialVersionsUseSourceAccountIDAndSkipAPIKey(t *testing.T) {
	models := newFakeModels()
	reader := &fakeCredentialVersions{versions: map[string]int64{"source-1": 7}}
	svc := newTestService(t, models, newManualClock(), func(opts *Options) { opts.CredentialVersions = reader })
	source := "source-1"
	authorized := testOAuthAccount("child-1", "sys")
	authorized.CredentialSourceAccountID = &source
	apiKeyAccount := testAccount("api-1", "sys")

	versions := svc.readCredentialVersions(context.Background(), []OpenAIAccountSecret{authorized, apiKeyAccount})
	if len(versions) != 1 || versions["source-1"] != 7 {
		t.Fatalf("versions = %v, want {source-1:7}", versions)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if len(reader.lastIDs) != 1 || reader.lastIDs[0] != "source-1" {
		t.Fatalf("读取 ID = %v, want [source-1]（api_key 账户应被跳过）", reader.lastIDs)
	}
}
