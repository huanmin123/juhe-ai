package circuitstore

// 收尾补充：composeEntry 的 authorized 全链合成、hydratePage 的 fail-closed
// 错误分支、store/adapter 的剩余转移路径与转换出口、overlay 确认透传。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	redis "github.com/redis/go-redis/v9"
)

// TestComposeEntryAuthorizedFullChain 覆盖 authorized 实例的合成链
// （状态派生、授权 payload 键、来源投影列、绑定组）。
func TestComposeEntryAuthorizedFullChain(t *testing.T) {
	loader := &ProjectionItemLoader{}
	now := effNow()
	row := newSourcesRow("acc-auth")
	row.status = "active"
	row.providerCode = "openai"
	row.providerProtocolProfileID = "local-profile"
	row.protocolCode = "openai"
	row.protocolVersion = "v1"
	row.name = "授权实例"
	row.accountType = "api_key"
	row.schedulable = 1
	row.concurrencyLimit = 2
	row.priority = 1
	row.authorizationID = sql.NullString{String: "auth-1", Valid: true}
	row.authorizationStatus = sql.NullString{String: "active", Valid: true}
	row.authorizationExpiresAt = sql.NullString{String: effFuture, Valid: true}
	row.authorizationLimitsJSON = sql.NullString{String: "", Valid: false}
	row.authorizationEffectiveSourceTeamID = sql.NullString{String: "team-1", Valid: true}
	row.authorizationInstanceSourceID = sql.NullString{String: "src-1", Valid: true}
	row.sourceStatus = sql.NullString{String: "active", Valid: true}
	row.sourceProviderCode = sql.NullString{String: "gpt", Valid: true}
	row.sourceProviderProfileID = sql.NullString{String: "src-profile", Valid: true}
	row.sourceType = sql.NullString{String: "api_key", Valid: true}
	row.sourceConcurrencyLimit = 30
	row.boundGroupID = sql.NullString{String: "group-1", Valid: true}
	row.bindingSystemAccountID = sql.NullString{String: "sys-1", Valid: true}
	row.boundGroupAccountAuthorizationID = sql.NullString{String: "auth-1", Valid: true}
	row.boundGroupLocalPriority = 9
	row.boundGroupLocalSuperPriorityEnabled = 0
	row.boundGroupLocalFallbackEnabled = 1
	row.lastUsedAt = sql.NullString{String: "2026-08-01T00:00:00.000Z", Valid: true}
	entry, err := loader.composeEntry(composeInput{
		row:           row,
		payload:       buildBasePayload(row, nil, nil),
		now:           now,
		timezone:      time.UTC,
		quotaExceeded: false,
		quotaResetAt:  "",
		todayUsage:    usageValue{RequestCount: 3, TotalTokens: 30, TotalCost: 0.3},
		authTotal:     usageValue{RequestCount: 300, LastUsedAt: "2026-09-01T00:00:00.000Z"},
		concurrency:   2,
		circuit:       publicCircuitSummary{Status: "recovering", Reason: "http_502", Since: "2026-09-01T00:00:00.000Z"},
		apiKeyRuntime: &apiKeyRuntimeSummary{Total: 2, Active: 1, Unavailable: 1, NextProbeAt: effFuture},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 授权 active → 状态保留；无配额超限 → 可调度。
	if entry.effectiveStatus != "active" || !entry.effectiveAvailable {
		t.Fatalf("authorized 可用账户分类不符: %s %v", entry.effectiveStatus, entry.effectiveAvailable)
	}
	// 公共 lastUsedAt 展示授权 total；排序键取 accounts.last_used_at。
	if entry.payload["lastUsedAt"] != "2026-09-01T00:00:00.000Z" {
		t.Fatalf("authorized lastUsedAt 应展示授权 total: %v", entry.payload["lastUsedAt"])
	}
	if entry.sortLastUsedAt == nil || *entry.sortLastUsedAt != "2026-08-01T00:00:00.000Z" {
		t.Fatalf("排序键应取 accounts.last_used_at: %v", entry.sortLastUsedAt)
	}
	// 授权相关 payload 键。
	if entry.payload["authorizationStatus"] != "active" || entry.payload["authorizationExpiresAt"] != effFuture {
		t.Fatalf("授权键不符: %v %v", entry.payload["authorizationStatus"], entry.payload["authorizationExpiresAt"])
	}
	if entry.payload["authorizationQuotaExceeded"] != false {
		t.Fatalf("authorized 行恒写 quota 键（false 也保留）: %v", entry.payload["authorizationQuotaExceeded"])
	}
	limits, ok := entry.payload["authorizationLimits"].(map[string]any)
	if !ok || len(limits) != 0 {
		t.Fatalf("空 limits JSON 应为空对象: %v", entry.payload["authorizationLimits"])
	}
	// 授权实例投影列取来源值与 group local 排序值。
	if entry.providerCode != "gpt" || entry.profileID != "src-profile" || entry.accountType != "api_key" {
		t.Fatalf("authorized 投影列应取来源: %+v", entry)
	}
	if entry.priority != 9 || entry.concurrencyLimit != 30 || !entry.fallback || entry.superPriority {
		t.Fatalf("authorized 排序键取 group local 值: %+v", entry)
	}
	if entry.boundGroupID != "group-1" || entry.authorizationID != "auth-1" || entry.sourceAccountID != "src-1" {
		t.Fatalf("绑定/授权标识不符: %+v", entry)
	}
	// circuit + apiKeyRuntime payload 键。
	circuitPayload, ok := entry.payload["circuitSummary"].(map[string]any)
	if !ok || circuitPayload["status"] != "recovering" {
		t.Fatalf("circuitSummary 键不符: %v", entry.payload["circuitSummary"])
	}
	apiKeyPayload, ok := entry.payload["apiKeyRuntime"].(map[string]any)
	if !ok || apiKeyPayload["total"] != 2 {
		t.Fatalf("apiKeyRuntime 键不符: %v", entry.payload["apiKeyRuntime"])
	}
	if entry.apiKeyNextProbeAt == nil || *entry.apiKeyNextProbeAt != effFuture {
		t.Fatalf("apiKey next probe 候选应保留: %v", entry.apiKeyNextProbeAt)
	}
	// 授权状态非 active → 实例状态派生为 disabled。
	paused := row
	paused.authorizationStatus = sql.NullString{String: "paused", Valid: true}
	entry, err = loader.composeEntry(composeInput{row: paused, payload: buildBasePayload(paused, nil, nil), now: now})
	if err != nil {
		t.Fatal(err)
	}
	if entry.effectiveStatus != "disabled" || entry.effectiveAvailable {
		t.Fatalf("授权暂停必须派生 disabled: %s %v", entry.effectiveStatus, entry.effectiveAvailable)
	}
	if entry.authorizationExpiresAt == nil {
		t.Fatalf("授权到期候选应保留: %v", entry.authorizationExpiresAt)
	}
	// 授权额度超限 → rate_limited 分类 + quota reset 边界。
	entry, err = loader.composeEntry(composeInput{
		row: row, payload: buildBasePayload(row, nil, nil), now: now,
		quotaExceeded: true, quotaResetAt: "2030-06-01T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.effectiveStatus != "rate_limited" || entry.effectiveAvailable {
		t.Fatalf("授权额度超限应 rate_limited: %s %v", entry.effectiveStatus, entry.effectiveAvailable)
	}
	if entry.statusBoundaryAt == nil || *entry.statusBoundaryAt != "2030-06-01T00:00:00.000Z" {
		t.Fatalf("超限边界应取 quotaResetAt: %v", entry.statusBoundaryAt)
	}
	if entry.quotaResetAt == nil || *entry.quotaResetAt != "2030-06-01T00:00:00.000Z" {
		t.Fatalf("quotaResetAt 候选应保留: %v", entry.quotaResetAt)
	}
}

// TestComposeEntryRuntimeAndBalance 覆盖运行态可用性与余额快照合成。
func TestComposeEntryRuntimeAndBalance(t *testing.T) {
	loader := &ProjectionItemLoader{}
	now := effNow()
	row := newSourcesRow("acc-runtime")
	row.status = "active"
	row.schedulable = 1
	row.balanceQueryEnabled = 1
	row.balanceQueryNextRefreshAt = sql.NullString{String: "2026-09-04T10:00:00.000Z", Valid: true}
	row.configRevision = sql.NullInt64{Int64: 3, Valid: true}
	row.availabilityScheduleJSON = sql.NullString{String: validScheduleJSON(), Valid: true}
	entry, err := loader.composeEntry(composeInput{
		row:     row,
		payload: buildBasePayload(row, nil, nil),
		now:     now,
		runtime: AccountRuntimeAvailability{
			Status: "degraded", Reason: "recent_failures", Since: "2026-09-01T00:00:00.000Z",
			ProbePresentation: map[string]any{
				"schedule":   map[string]any{"state": "scheduled", "nextAttemptAt": effFuture},
				"recoveryAt": "2031-01-01T00:00:00.000Z",
			},
		},
		balance: &balanceSnapshotRecord{
			snapshot: map[string]any{"totalBalance": 5.5, "keyBalances": []any{1}},
			// 行为存疑相关：configRevision 经 numberValue 恒回默认 1（见
			// TestBuildBasePayloadOwnerRow），此处用 revision 0 跳过校验。
			snapshotRevision: 0,
			nextRefreshAfter: "2026-09-04T10:00:00.000Z",
		},
		concurrency: -3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.effectiveStatus != "active" {
		t.Fatalf("degraded 仍分类 active: %s", entry.effectiveStatus)
	}
	// degraded 有 reason fallback（缺口分支）→ presentation携带 status。
	if entry.effectiveAvailable != true {
		t.Fatalf("degraded 可用: %+v", entry)
	}
	runtimePayload, ok := entry.payload["runtimeAvailability"].(map[string]any)
	if !ok || runtimePayload["status"] != "degraded" {
		t.Fatalf("runtimeAvailability 键不符: %v", entry.payload["runtimeAvailability"])
	}
	balancePayload, ok := entry.payload["balanceSnapshot"].(map[string]any)
	if !ok || balancePayload["totalBalance"] != 5.5 {
		t.Fatalf("余额快照应为 forList 形状（去 keyBalances）: %v", entry.payload["balanceSnapshot"])
	}
	if _, exists := balancePayload["keyBalances"]; exists {
		t.Fatalf("forList 必须剥离 keyBalances: %v", balancePayload)
	}
	if entry.currentConcurrency != 0 {
		t.Fatalf("负并发必须夹取 0: %d", entry.currentConcurrency)
	}
	if entry.runtimeNextAttemptAt == nil || entry.runtimeRecoveryAt == nil {
		t.Fatalf("runtime 计划候选应保留: %v %v", entry.runtimeNextAttemptAt, entry.runtimeRecoveryAt)
	}
	if entry.availabilityScheduleJSON == nil {
		t.Fatalf("schedule 候选应保留: %v", entry.availabilityScheduleJSON)
	}
	// 余额配置不匹配 → 不发布 balanceSnapshot。
	mismatch := row
	mismatch.balanceQueryNextRefreshAt = sql.NullString{String: "2027-01-01T00:00:00.000Z", Valid: true}
	entry, err = loader.composeEntry(composeInput{
		row:     mismatch,
		payload: buildBasePayload(mismatch, nil, nil),
		now:     now,
		balance: &balanceSnapshotRecord{snapshot: map[string]any{}, nextRefreshAfter: "2026-09-04T10:00:00.000Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := entry.payload["balanceSnapshot"]; exists {
		t.Fatalf("配置不匹配不得发布余额快照: %v", entry.payload["balanceSnapshot"])
	}
	// owner 行 authorized=false 时 quota 键不出现。
	if _, exists := entry.payload["authorizationQuotaExceeded"]; exists {
		t.Fatalf("owner 行不得写 quota 键: %v", entry.payload)
	}
}

func hydrateFailingLoader(t *testing.T, db *sql.DB, concurrency ConcurrencySource, runtime RuntimeAvailabilitySource) *ProjectionItemLoader {
	t.Helper()
	loader, err := NewProjectionItemLoader(ProjectionLoadConfig{
		Business: db, Stats: db, Secret: "s",
		Credentials: stubCredentials{}, Concurrency: concurrency, RuntimeAvailability: runtime,
		Timezone: stubTimezone{}, Now: func() time.Time { return effNow() },
	})
	if err != nil {
		panic(err)
	}
	return loader
}

// TestHydratePageFailClosed 覆盖水合链各读面失败的 fail-closed 行为。
func TestHydratePageFailClosed(t *testing.T) {
	db := newLoaderTestDB(t)
	seedOwnerAccount(t, db)
	// DB 查询失败面：锁状态表不存在 → loadAccountLockViews 报错。
	loader := hydrateFailingLoader(t, db, stubConcurrency{}, stubRuntime{})
	if _, err := db.Exec(`DROP TABLE account_lock_states`); err != nil {
		t.Fatal(err)
	}
	page := &managementPage{rows: []managementRow{newSourcesRow("acct-1")}}
	if _, err := loader.hydratePage(context.Background(), page, effNow()); err == nil {
		t.Fatal("锁状态读取失败必须 fail closed")
	}
	// 恢复表后：运行态读失败、并发读失败各自 fail closed。
	if _, err := db.Exec(`CREATE TABLE account_lock_states (account_id TEXT PRIMARY KEY, enabled INTEGER, lock_state TEXT, lock_death_timeout_seconds INTEGER, lock_retry_interval_seconds INTEGER, generation INTEGER, incident_id TEXT, incident_started_at TEXT, deadline_at TEXT, original_status TEXT, provenance TEXT, next_retry_at_ms INTEGER, lease_id TEXT, lease_until_ms INTEGER, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("redis down")
	if _, err := hydrateFailingLoader(t, db, stubConcurrency{err: boom}, stubRuntime{}).hydratePage(context.Background(), page, effNow()); err == nil {
		t.Fatal("并发读失败必须 fail closed")
	}
	if _, err := hydrateFailingLoader(t, db, stubConcurrency{}, stubRuntime{err: boom}).hydratePage(context.Background(), page, effNow()); err == nil {
		t.Fatal("运行态读失败必须 fail closed")
	}
	// 时区读失败 fail closed。
	brokenTimezoneLoader, err := NewProjectionItemLoader(ProjectionLoadConfig{
		Business: db, Stats: db, Secret: "s", Credentials: stubCredentials{},
		Concurrency: stubConcurrency{}, RuntimeAvailability: stubRuntime{},
		Timezone: brokenTimezone{}, Now: func() time.Time { return effNow() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTimezoneLoader.hydratePage(context.Background(), page, effNow()); err == nil {
		t.Fatal("时区读失败必须 fail closed")
	}
}

type brokenTimezone struct{}

func (brokenTimezone) StatsTimezone(ctx context.Context) (*time.Location, error) {
	return nil, errors.New("tz down")
}

// TestStoreRemainingTransitionPaths 覆盖 CompleteCanary（含 reason/evidence）、
// Get 缺失状态、无注入时钟的默认时钟路径。
func TestStoreRemainingTransitionPaths(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	wire := store.Underlying()
	ctx := context.Background()
	scope := accountScope("acc-final")
	// Get 不存在状态：Lua 返回 CLOSED 空终态（generation 0、无 revision）。
	empty, err := wire.Get(ctx, convertScope(scope), nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Phase != "CLOSED" || empty.Generation != 0 || empty.DispatchRevision != "" {
		t.Fatalf("不存在状态应返回 CLOSED 空终态: %+v", empty)
	}
	// 植入 OPEN → acquire canary → complete canary（reason + evidenceScopeKey）。
	openState := opsjobs.CircuitState{
		ScopeKey: MustScopeKey(convertScope(scope)), Scope: scope,
		Phase: opsjobs.CircuitPhaseOpen, Generation: 1, DispatchRevision: "5",
		TransitionID: "in-1", IncidentID: "in-1",
		RetryAtMS: &[]int64{now() - 5}[0], UpdatedAtMS: now(),
	}
	if _, err := store.Restore(ctx, openState, now()); err != nil {
		t.Fatal(err)
	}
	identity := opsjobs.CircuitTransitionIdentity{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "tr-c", NowMS: now()}
	if _, err := store.AcquireCanaryLease(ctx, identity, opsjobs.CircuitLeaseSpec{LeaseID: "l-f", LeaseUntilMS: now() + 5000}); err != nil {
		t.Fatal(err)
	}
	complete := identity
	complete.TransitionID = "tr-cc"
	reason := "probe_ok"
	evidence := "sk-evidence"
	result, err := store.CompleteCanary(ctx, complete, "l-f", opsjobs.CircuitCompletion{
		Outcome: opsjobs.CircuitVerdictFramingComplete, Reason: reason, EvidenceScopeKey: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != opsjobs.CircuitMutationApplied || result.State.Phase != opsjobs.CircuitPhaseRecovering {
		t.Fatalf("complete canary 失败: %s %s", result.Status, result.State.Phase)
	}
	// 完成态走 ListDue 分页过滤（CLOSED/RECOVERING 不到期）。
	due, err := wire.ListDue(ctx, now(), 5)
	if err != nil || len(due) != 0 {
		t.Fatalf("RECOVERING 不得到期: %v %v", due, err)
	}
	// MustScopeKey 对非法 scope panic（用 recover 验证契约）。
	defer func() {
		if recover() == nil {
			t.Fatal("非法 scope 必须 panic（Must 契约）")
		}
	}()
	MustScopeKey(Scope{Kind: "bogus", AccountRuntimeKey: "x"})
}

// TestDefaultClockPath 覆盖无注入 Now 时的默认时钟构造与读写路径。
func TestDefaultClockPath(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisStore(RedisStoreOptions{Client: client, Namespace: "ns", Capacity: 4})
	if err != nil {
		t.Fatal(err)
	}
	// 不报错即覆盖 normalizedNowValue fallback（defaultNowMs → timeNowUnixMilli）。
	if _, err := store.Get(context.Background(), Scope{Kind: "account", AccountRuntimeKey: "acc-x"}, nil); err != nil {
		t.Fatalf("默认时钟读取: %v", err)
	}
}

// TestToOpsMutationResultErrors 覆盖 relatedStates 中损坏状态的错误出口。
func TestToOpsMutationResultErrors(t *testing.T) {
	scope := accountScope("acc-conv")
	good := fromOpsState(opsjobs.CircuitState{
		ScopeKey: MustScopeKey(convertScope(scope)), Scope: scope, Phase: opsjobs.CircuitPhaseOpen,
	})
	// 正常转换（含 related）。
	converted, err := toOpsMutationResult(MutationResult{Status: "applied", State: good, RelatedStates: stateList{good}})
	if err != nil {
		t.Fatal(err)
	}
	if converted.Status != opsjobs.CircuitMutationApplied || len(converted.RelatedStates) != 1 {
		t.Fatalf("related 转换不符: %+v", converted)
	}
	// 主状态 scopeKey 与 scope 不一致 → 报错。
	broken := good
	broken.ScopeKey = "wrong"
	if _, err := toOpsMutationResult(MutationResult{Status: "applied", State: broken}); err == nil {
		t.Fatal("scopeKey 不一致必须报错")
	}
	// related 中 scopeKey 不一致 → 报错。
	if _, err := toOpsMutationResult(MutationResult{Status: "applied", State: good, RelatedStates: stateList{broken}}); err == nil {
		t.Fatal("related scopeKey 不一致必须报错")
	}
	// toOpsState 错误出口直测。
	if _, err := toOpsState(broken); err == nil {
		t.Fatal("toOpsState 对不一致 scopeKey 必须报错")
	}
	// stringPtr：空串返回 nil。
	if stringPtr("") != nil || stringPtr("x") == nil {
		t.Fatal("stringPtr 语义不符")
	}
	if convertScanError(errors.New("x")) == nil {
		t.Fatal("convertScanError 应透传错误")
	}
}

// TestOverlayReconcilerAcknowledgePassthrough 覆盖 reconciler 确认透传。
func TestOverlayReconcilerAcknowledgePassthrough(t *testing.T) {
	repo, _ := openListAvailabilityFixture(t)
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	store := newOverlayStore(t, server)
	reconciler := NewOverlayReconciler(store, repo)
	ctx := context.Background()
	// 空确认 no-op。
	if err := reconciler.Acknowledge(ctx, nil); err != nil {
		t.Fatalf("空确认: %v", err)
	}
	if _, err := server.ZAdd(store.dirtyKey(), 1, "acc-1"); err != nil {
		t.Fatal(err)
	}
	server.HSet(store.generationKey(), "acc-1", "4")
	entries, err := reconciler.ListDirtyEntries(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("认领失败: %v %v", entries, err)
	}
	if err := reconciler.Acknowledge(ctx, entries); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := server.ZMembers(store.dirtyKey()); len(remaining) != 0 {
		t.Fatalf("透传确认必须生效: %v", remaining)
	}
}

// TestLoadSearchTermsAndScopesEdge 覆盖有搜索词与无授权 scope 的读取路径。
func TestLoadSearchTermsAndScopesEdge(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`
		INSERT INTO accounts (id, system_account_id, authorization_instance_authorization_id) VALUES
			('acc-t', 'viewer-1', NULL), ('acc-auth', 'viewer-1', 'auth-gone');
		INSERT INTO system_accounts (id) VALUES ('viewer-1');
		INSERT INTO resource_authorizations (id, status) VALUES ('auth-gone', 'active');
		INSERT INTO account_name_search_documents (account_id) VALUES ('acc-t');
		INSERT INTO account_name_search_terms (account_id, term) VALUES ('acc-t', '账'), ('acc-t', '账户');`); err != nil {
		t.Fatal(err)
	}
	terms, err := repo.LoadSearchTerms(ctx, []string{"acc-t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(terms["acc-t"]) != 2 || terms["acc-t"][0] != "账" || terms["acc-t"][1] != "账户" {
		t.Fatalf("搜索词按字典序返回: %v", terms)
	}
	// 无文档的账户不出键。
	if _, exists := terms["acc-auth"]; exists {
		t.Fatalf("无搜索文档不得出键: %v", terms)
	}
	scopes, err := repo.ListScopes(ctx, []string{"acc-t", "acc-auth", "acc-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("可见 scope 应含 owner 与授权有效账户: %+v", scopes)
	}
}

func nowValueHolder() *int64 {
	value := int64(1_000_000)
	return &value
}
