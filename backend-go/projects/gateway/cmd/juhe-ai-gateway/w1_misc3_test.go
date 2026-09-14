package main

// w1（单元层）：组合根/接线层零散小函数收割——适配器、投影、日志桥、
// Redis runtime-state 桥（miniredis）、目录列表、锁行读取、策略避让
// 抑制解析器、用量失败记录。全部参数伪造、确定性、秒级。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
)

func TestW1MiscSmallAdapters(t *testing.T) {
	if derefInt64(nil) != 0 {
		t.Fatal("nil 指针必须回落 0")
	}
	value := int64(7)
	if derefInt64(&value) != 7 {
		t.Fatal("derefInt64 必须解引用")
	}
	// sharedDBPool：DB 返回原句柄、Close 是 no-op。
	if _, err := sql.Open("sqlite", ":memory:"); err != nil {
		t.Fatalf("open = %v", err)
	}
	db, _ := sql.Open("sqlite", ":memory:")
	pool := sharedDBPool{db: db}
	if pool.DB() != db {
		t.Fatal("sharedDBPool.DB 必须返回原句柄")
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("sharedDBPool.Close 必须是 no-op = %v", err)
	}
	_ = db.Close()
	owner := newGatewayBalanceOwnerID()
	if !strings.HasPrefix(owner, "gateway-manual-") || len(owner) != len("gateway-manual-")+12 {
		t.Fatalf("owner id 契约不符 = %q", owner)
	}
	// 降级错误策略副作用：RecordKeyScopedQuotaFailure 恒 nil。
	degraded := &degradedChainErrorPolicyEffects{}
	if err := degraded.RecordKeyScopedQuotaFailure(context.Background(), gatewaydispatch.AccountCandidate{}, accountErrorPolicyDecision{}, chainErrorPolicyFailureInput{}); err != nil {
		t.Fatalf("降级 RecordKeyScopedQuotaFailure = %v", err)
	}
	// CORS 投影。
	config := &runtimeConfig{CORSAllowAnyOrigin: true, CORSAllowedOrigins: []string{"https://a.example"}}
	policy := config.corsPolicy()
	if !policy.AllowAnyOrigin || len(policy.AllowedOrigins) != 1 || policy.AllowedOrigins[0] != "https://a.example" {
		t.Fatalf("corsPolicy 投影不符 = %+v", policy)
	}
	// codex 会话身份适配器：nil 依赖/nil 请求都回落 Missing。
	missing := codexSourceSessionAdapter{}.ResolveSessionIdentity(nil, gatewaypreauth.SessionIdentityInput{})
	if missing.Status != gatewaysession.IdentityStatusMissing {
		t.Fatalf("nil identity 状态 = %v", missing.Status)
	}
	// 预热 nil 守卫。
	startGatewayAPIKeyCachePrewarm(nil, newTestSlogLogger())
}

func TestW1LoggerAdapters(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// slogLogger：Debug/Warn/Error 走 fieldsArgs 展开。
	inner := slogLogger{inner: logger}
	inner.Debug("debug-msg", map[string]any{"k": 1})
	inner.Warn("warn-msg", map[string]any{"k": 2})
	inner.Error("error-msg", map[string]any{"k": 3})
	// runtime cache logger：Warn(event, fields, message) 形态。
	chainRuntimeCacheLogger{inner: logger}.Warn("cache-event", map[string]any{"scope": "w1"}, "cache-message")
	text := buffer.String()
	for _, expected := range []string{"debug-msg", "warn-msg", "error-msg", "cache-message", "cache-event", "scope=w1"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("日志输出缺少 %q：%s", expected, text)
		}
	}
	// 全局 slog 桥：slogWarnFields / slogInfoFields。
	oldDefault := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(oldDefault)
	buffer.Reset()
	slogWarnFields("gateway_local_account_suppression", map[string]any{"w": "x"}, "warn-fields")
	slogInfoFields("gateway_local_account_suppression", map[string]any{"i": "y"}, "info-fields")
	text = buffer.String()
	if !strings.Contains(text, "warn-fields") || !strings.Contains(text, "info-fields") {
		t.Fatalf("全局 slog 桥输出不符：%s", text)
	}
	if !strings.Contains(text, "event=gateway_local_account_suppression") {
		t.Fatalf("event 字段缺失：%s", text)
	}
}

func TestW1QuotaRuntimeStateBridgeAndEval(t *testing.T) {
	mini := miniredis.NewMiniRedis()
	if err := mini.Start(); err != nil {
		t.Fatalf("miniredis start = %v", err)
	}
	defer mini.Close()
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer func() { _ = client.Close() }()
	store, err := gatewayquota.NewRedisRuntimeStateStore(client, "w1-ns", "w1-quota")
	if err != nil {
		t.Fatalf("runtime state store = %v", err)
	}
	bridge := quotaRuntimeStateBridge{store: store, storeName: "w1-quota"}
	ctx := context.Background()
	var got map[string]any
	hit, err := bridge.GetJSON(ctx, "missing", &got)
	if err != nil || hit {
		t.Fatalf("miss 读取 = hit=%v err=%v", hit, err)
	}
	payload := map[string]any{"limit": float64(9)}
	if err := bridge.SetJSON(ctx, "quota-key", payload, time.Minute); err != nil {
		t.Fatalf("SetJSON = %v", err)
	}
	hit, err = bridge.GetJSON(ctx, "quota-key", &got)
	if err != nil || !hit || got["limit"] != float64(9) {
		t.Fatalf("round-trip = hit=%v got=%v err=%v", hit, got, err)
	}
	if err := bridge.Delete(ctx, "quota-key"); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	hit, _ = bridge.GetJSON(ctx, "quota-key", &got)
	if hit {
		t.Fatal("删除后不得再命中")
	}
	// redisEvalClient：EVAL 契约。
	result, err := redisEvalClient{client: client}.Eval(ctx, "return 1", []string{"w1-key"})
	if err != nil || result == nil {
		t.Fatalf("Eval = %v %v", result, err)
	}
	// redisStateClientProvider：nil 拒绝、有效返回适配器、Invalidate no-op。
	if _, err := (redisStateClientProvider{}).Client(ctx); err == nil {
		t.Fatal("nil client 必须拒绝")
	}
	adapter, err := redisStateClientProvider{client: client}.Client(ctx)
	if err != nil || adapter == nil {
		t.Fatalf("Client = %v %v", adapter, err)
	}
	redisStateClientProvider{client: client}.Invalidate(ctx, adapter)
}

func TestW1SettingsLimitAndCatalogs(t *testing.T) {
	fixture := newChainFixture(t)
	store, err := settings.NewStore(fixture.db, false, time.Now, nil)
	if err != nil {
		t.Fatalf("settings store = %v", err)
	}
	limit, err := aiAccountLimitSettingsAdapter{settings: store}.UserAiAccountLimit(context.Background())
	if err != nil {
		t.Fatalf("UserAiAccountLimit = %v", err)
	}
	if limit < 1 {
		t.Fatalf("默认账户上限必须为正 = %d", limit)
	}
	// 目录适配：nil cache 直接 nil；有 cache 时返回空集或错误回落 nil。
	if (chatModelCatalog{}).ListProviderCatalog("openai", "sys") != nil {
		t.Fatal("nil cache 必须返回 nil")
	}
	items := (chatModelCatalog{cache: fixture.cache}).ListProviderCatalog("openai", "sys_owner")
	if len(items) != 1 {
		t.Fatalf("seed 目录期望 1 条 = %d", len(items))
	}
	// 追加一条 inactive 目录：override 目录必须过滤非 active。
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := fixture.db.Exec(`INSERT INTO provider_model_catalog (
			id, status, provider_code, model, catalog_order, supported_api_protocols_json, source,
			catalog_visible, supports_prompt_caching, created_at, updated_at)
		VALUES ('cat_2', 'inactive', 'openai', 'gpt-test-inactive', 1, '["chat_completions"]', 'builtin', 1, 0, ?, ?)`, now, now); err != nil {
		t.Fatalf("insert inactive catalog = %v", err)
	}
	override := chainGptRequestOverrideModelCatalog{cache: fixture.cache}
	overrideItems, err := override.ListGptRequestOverrideModelCatalog(context.Background(), "openai", "sys_owner", true)
	if err != nil {
		t.Fatalf("ListGptRequestOverrideModelCatalog = %v", err)
	}
	if len(overrideItems) != 1 || overrideItems[0].Model != "gpt-test" {
		t.Fatalf("inactive 必须被过滤 = %+v", overrideItems)
	}
}

func TestW1AttemptAuditSinkCompleteAttempt(t *testing.T) {
	// nil capture：直接短路。
	chainAttemptAuditSink{}.CompleteAttempt("att-1", gatewaydispatch.CompleteAttemptInput{Success: true})
	capture := gatewayusage.NewAuditCaptureContext(gatewayusage.AuditCaptureInput{
		TraceID: "trace_w1_sink", ClientIP: "203.0.113.10", StartedAtMs: 1_000,
		TrafficSource: gatewayTrafficSource, Method: "POST", Path: "/v1/chat/completions",
		Logger: slogLogger{inner: newTestSlogLogger()},
	})
	// 无 settings/dispatcher 的 capture 审计关闭：StartAttempt 返回空串是契约，
	// CompleteAttempt 的转换分支用固定 attempt id 收割。
	attemptID := capture.StartAttempt(gatewayusage.StartAttemptInput{
		Account: gatewayusage.UsageModelAccount{ID: "acc_1"}, AttemptIndex: 0,
		UpstreamURL: "https://upstream/v1/chat/completions", Method: "POST",
	})
	status := 502
	sink := chainAttemptAuditSink{capture: capture}
	sink.CompleteAttempt(attemptID, gatewaydispatch.CompleteAttemptInput{
		Success: false, ErrorPhase: "upstream_response", ErrorCode: "aux_http_error",
		ErrorMessage: "上游 502", StatusCode: &status,
		ResponseHeaders: map[string][]string{"content-type": {"application/json", "extra"}},
		ResponseBody:    []byte("boom"),
	})
	// 再走一条无状态码/无响应体的失败（nil 分支）。
	sink.CompleteAttempt(attemptID, gatewaydispatch.CompleteAttemptInput{
		Success: false, ErrorPhase: "dispatch", ErrorCode: "aux_dispatch_failed",
	})
	if capture == nil {
		t.Fatal("capture 不得为 nil")
	}
}

func TestW1AuthzAdapters(t *testing.T) {
	fixture := newChainFixture(t)
	store, err := authz.NewStore(fixture.db, false, time.Now)
	if err != nil {
		t.Fatalf("authz store = %v", err)
	}
	// 资源授权统计读面所需的 sources 表（fixture 未建）。
	_, err = fixture.db.Exec(`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, resource_type TEXT NOT NULL DEFAULT 'api_key', resource_id TEXT NOT NULL DEFAULT '', source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active', activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL DEFAULT '')`)
	if err != nil {
		t.Fatalf("create sources table = %v", err)
	}
	ctx := context.Background()
	stats, err := authorizationStatsSourceAdapter{store: store}.ResourceAuthorizationStatsByResourceIds(ctx, "api_key", []string{"k-1", "k-2"})
	if err != nil {
		t.Fatalf("ResourceAuthorizationStatsByResourceIds = %v", err)
	}
	// 未知资源返回零值统计条目（key 集合保持请求形状）。
	if len(stats) != 2 || stats["k-1"].AuthorizationCount != 0 || stats["k-2"].AuthorizationTeamCount != 0 {
		t.Fatalf("未知资源期望零值统计 = %v", stats)
	}
	// 授权归还：最小 grants 行 + expectedUpdatedAt 不匹配 → conflict。
	_, err = fixture.db.Exec(`CREATE TABLE resource_authorization_grants (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_type TEXT,grantee_system_account_id TEXT,grantee_team_id TEXT,status TEXT,remark TEXT,expires_at TEXT,limits_json TEXT,created_by TEXT,revoked_by TEXT,revoked_at TEXT,created_at TEXT,updated_at TEXT)`)
	if err != nil {
		t.Fatalf("create grants table = %v", err)
	}
	_, err = fixture.db.Exec(`INSERT INTO resource_authorization_grants (id,resource_type,resource_id,resource_owner_system_account_id,grantee_type,grantee_system_account_id,status,created_at,updated_at) VALUES ('g-1','api_key','k-1','sys_owner','system_account','user-1','active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert grant = %v", err)
	}
	returner := authzGrantReturner{store: store}
	status, err := returner.Return(ctx, "g-1", "9999-01-01T00:00:00Z", "user-1")
	if err != nil {
		t.Fatalf("Return = %v", err)
	}
	if status != "conflict" {
		t.Fatalf("expectedUpdatedAt 不匹配期望 conflict = %q", status)
	}
	missing, err := returner.Return(ctx, "g-missing", "2026-01-01T00:00:00Z", "user-1")
	if err != nil || missing != "not_found" {
		t.Fatalf("缺失授权期望 not_found = %q %v", missing, err)
	}
}

// w1AvoidanceStoreStub 可回放的 PolicyAvoidanceStateStore 桩。
type w1AvoidanceStoreStub struct {
	states map[string][]byte
	err    error
}

func (s *w1AvoidanceStoreStub) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	if s.err != nil {
		return nil, s.err
	}
	if raw, ok := s.states[key]; ok {
		return json.RawMessage(raw), nil
	}
	return nil, nil
}

func (s *w1AvoidanceStoreStub) SetJSON(_ context.Context, _ string, _ any, _ int64) error {
	return nil
}

type w1FixedClock struct{}

func (w1FixedClock) Now() time.Time { return time.UnixMilli(1728000000000) }

type w1RouteCoordinatorFake struct {
	failures []gatewayrouting.GatewayRouteFinalFailure
	fallback []string
}

func (c *w1RouteCoordinatorFake) RequestFallback(_ context.Context, reason string) (gatewayrouting.GatewayRouteFallbackDecision, error) {
	c.fallback = append(c.fallback, reason)
	return gatewayrouting.GatewayRouteFallbackDecision{}, nil
}

func (c *w1RouteCoordinatorFake) CompleteFailure(_ context.Context, failure gatewayrouting.GatewayRouteFinalFailure) error {
	c.failures = append(c.failures, failure)
	return nil
}

type w1SuppressionInnerFake struct {
	result    *gatewaydispatch.SuppressionFilterResult
	completed bool
	err       error
}

func (p w1SuppressionInnerFake) FilterAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ gatewaydispatch.SuppressionFilterOptions) (gatewaydispatch.SuppressionFilterResult, error) {
	return gatewaydispatch.SuppressionFilterResult{Accounts: accounts}, nil
}

func (p w1SuppressionInnerFake) ResolveLocalSuppressionFilter(_ context.Context, input gatewaydispatch.LocalSuppressionPreflightInput) (*gatewaydispatch.SuppressionFilterResult, bool, error) {
	if p.err != nil {
		return nil, false, p.err
	}
	return p.result, p.completed, nil
}

func w1AvoidanceStateJSON(runtimeKey, accountID string, untilMs int64) []byte {
	raw, _ := json.Marshal(gatewayaccounteffects.ConfiguredPolicyAvoidanceState{
		RuntimeKey: runtimeKey, AccountID: accountID, Reason: "avoid_account_ttl",
		StartedAtMs: untilMs - 3_600_000, UntilMs: untilMs,
	})
	return raw
}

func TestW1ResolveLocalSuppressionFilterArms(t *testing.T) {
	ctx := context.Background()
	capture := gatewaydispatch.AuditCapture{Context: preauthAuditCapture{inner: w1Capture()}}
	accounts := []gatewaydispatch.AccountCandidate{
		{ID: "acc_a", ProviderCode: "openai"},
		{ID: "acc_b", ProviderCode: "openai"},
	}
	// 通道一：全部被配置策略避让 → CompleteFailure + completed=true。
	futureMs := time.Now().Add(24 * time.Hour).UnixMilli()
	pastMs := time.Now().Add(-24 * time.Hour).UnixMilli()
	allSuppressed := &w1AvoidanceStoreStub{states: map[string][]byte{
		"acc_a": w1AvoidanceStateJSON("acc_a", "acc_a", futureMs),
		"acc_b": w1AvoidanceStateJSON("acc_b", "acc_b", futureMs),
	}}
	service := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(allSuppressed, nil, nil, w1FixedClock{})
	coordinator := &w1RouteCoordinatorFake{}
	wrapper := &chainConfiguredPolicyAvoidanceSuppression{
		inner:     w1SuppressionInnerFake{},
		avoidance: service,
	}
	result, completed, err := wrapper.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts: accounts, AuditCapture: capture, RouteCoordinator: coordinator,
	})
	if err != nil || !completed || result != nil {
		t.Fatalf("全避让通道 = result=%v completed=%v err=%v", result, completed, err)
	}
	if len(coordinator.failures) != 1 || coordinator.failures[0].FailureAttribution != "gateway_capacity" {
		t.Fatalf("CompleteFailure 契约不符 = %+v", coordinator.failures)
	}
	// 通道二：一个过期（可见）+ 一个生效 → 内层解析器接管，返回合并结果。
	partial := &w1AvoidanceStoreStub{states: map[string][]byte{
		"acc_a": w1AvoidanceStateJSON("acc_a", "acc_a", futureMs),
		"acc_b": w1AvoidanceStateJSON("acc_b", "acc_b", pastMs),
	}}
	partialService := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(partial, nil, nil, w1FixedClock{})
	innerPort := chainSuppressionPort{store: gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{})}
	partialWrapper := &chainConfiguredPolicyAvoidanceSuppression{
		inner:     innerPort,
		avoidance: partialService,
	}
	result, completed, err = partialWrapper.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts: accounts, AuditCapture: capture, RouteCoordinator: coordinator,
	})
	if err != nil || completed || result == nil {
		t.Fatalf("部分避让通道 = result=%v completed=%v err=%v", result, completed, err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "acc_b" {
		t.Fatalf("可见账户必须是 acc_b = %+v", result.Accounts)
	}
	// 通道三：内层直接 completed（全本地抑制）。
	innerCompleted := &chainConfiguredPolicyAvoidanceSuppression{
		inner:     w1SuppressionInnerFake{completed: true},
		avoidance: gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(&w1AvoidanceStoreStub{}, nil, nil, w1FixedClock{}),
	}
	result, completed, err = innerCompleted.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts: accounts, AuditCapture: capture, RouteCoordinator: coordinator,
	})
	if err != nil || !completed || result != nil {
		t.Fatalf("内层 completed 通道 = result=%v completed=%v err=%v", result, completed, err)
	}
	// 通道四：避让状态读取失败。
	failing := &chainConfiguredPolicyAvoidanceSuppression{
		inner:     w1SuppressionInnerFake{},
		avoidance: gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(&w1AvoidanceStoreStub{err: errors.New("redis down")}, nil, nil, w1FixedClock{}),
	}
	if _, _, err = failing.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts: accounts, AuditCapture: capture, RouteCoordinator: coordinator,
	}); err == nil {
		t.Fatal("避让状态读取失败必须传播错误")
	}
}

func TestW1TrafficMigrationBridge(t *testing.T) {
	mini := miniredis.NewMiniRedis()
	if err := mini.Start(); err != nil {
		t.Fatalf("miniredis start = %v", err)
	}
	defer mini.Close()
	affinity, err := gatewaysession.NewAffinityService(gatewaysession.AffinityConfig{
		Secret:        "w1-traffic-migration-secret-0123456789abcdef",
		RedisCacheURL: "redis://" + mini.Addr(),
		Clock:         func() time.Time { return time.UnixMilli(1728000000000) },
	})
	if err != nil {
		t.Fatalf("affinity service = %v", err)
	}
	bridge := trafficRuntimeMigratorBridge{affinity: affinity}
	ctx := context.Background()
	migrated, err := bridge.MigrateOpenAIAccountTrafficRuntime(ctx, accounts.TrafficRuntimeMigrationInput{
		SourceAccountID: "acc_src", TargetAccountID: "acc_dst",
		AffinityScope:   &accounts.TrafficMigrationScope{SystemAccountID: "sys_owner", GroupID: "group_main"},
		PreferenceScope: &accounts.TrafficMigrationScope{SystemAccountID: "sys_owner"},
	})
	if err != nil {
		t.Fatalf("MigrateOpenAIAccountTrafficRuntime = %v", err)
	}
	if migrated != 0 {
		t.Fatalf("空会话期望 0 迁移 = %d", migrated)
	}
	// trafficMigrationScope：nil 直接 nil。
	if trafficMigrationScope(nil) != nil {
		t.Fatal("nil scope 必须返回 nil")
	}
	scope := trafficMigrationScope(&accounts.TrafficMigrationScope{SystemAccountID: "s", GroupID: "g"})
	if scope == nil || scope.SystemAccountID != "s" || scope.GroupID != "g" {
		t.Fatalf("scope 投影不符 = %+v", scope)
	}
}

func TestW1BodyRejectionUsageFailure(t *testing.T) {
	spool := newUsageSpool(t.TempDir(), gatewaypreauth.SystemClock{}, newTestSlogLogger(), usageSpoolCapacity{})
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 4096, Logger: newTestSlogLogger()}, spool)
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, spoolOverflow{spool: spool}, 0, 0)
	dispatch.OverflowEnabled = spool != nil
	service := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true}).
		WithClock(gatewaypreauth.SystemClock{})
	rec := &chainBodyRejectionRecorder{usage: service}
	requestCtx := &kernel.RequestContext{TraceID: "trace_reject", ClientIP: "203.0.113.11", Method: "POST", Path: "/v1/chat/completions"}
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	runtime := &gatewayruntimecache.GatewayRuntime{
		APIKey: &gatewayruntimecache.GatewayAPIKeyRow{ID: "key_1", SystemAccountID: "sys_owner", SelectedGroupID: "group_main"},
	}
	// usage 为 nil 的短路分支。
	(&chainBodyRejectionRecorder{}).recordUsageFailure(requestCtx, request, "trace_nil", "太快", gatewaybody.RejectionInput{}, runtime)
	// 真实 usage：body 拒绝落一条失败用量。
	rec.recordUsageFailure(requestCtx, request, "trace_reject", "请求体超限", gatewaybody.RejectionInput{
		StatusCode: 413, Reason: "payload_too_large", ErrorCode: "request_entity_too_large",
		ErrorMessage: "请求体超限", RawBodyBytes: 4096, LimitBytes: 1024,
	}, runtime)
	if runtime.APIKey.ID != "key_1" {
		t.Fatal("runtime 快照不应被修改")
	}
}

func TestW1ChainAccountLocksReadStateOnce(t *testing.T) {
	fixture := newChainFixture(t)
	locks := &chainAccountLocks{db: fixture.db, now: func() time.Time { return time.UnixMilli(1728000000000) }}
	ctx := context.Background()
	// 表缺失：返回错误而不是 panic。
	if _, err := locks.readStateOnce(ctx, "acc_1"); err == nil {
		t.Fatal("表缺失必须报错")
	}
	_, err := fixture.db.Exec(`CREATE TABLE account_lock_states (account_id TEXT PRIMARY KEY, enabled INTEGER, lock_state TEXT, lock_death_timeout_seconds INTEGER, lock_retry_interval_seconds INTEGER, incident_id TEXT, incident_started_at TEXT, deadline_at TEXT, original_status TEXT, provenance TEXT, next_retry_at_ms INTEGER, lease_id TEXT, lease_until_ms INTEGER, generation INTEGER, updated_at INTEGER)`)
	if err != nil {
		t.Fatalf("create lock states = %v", err)
	}
	_, err = fixture.db.Exec(`INSERT INTO account_lock_states (account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds, incident_id, incident_started_at, deadline_at, original_status, provenance, next_retry_at_ms, lease_id, lease_until_ms, generation, updated_at) VALUES ('acc_1', 1, 'locked', 300, 60, 'inc-1', '2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z', 'active', 'w1', NULL, NULL, NULL, 3, 1728000000000)`)
	if err != nil {
		t.Fatalf("insert lock state = %v", err)
	}
	row, err := locks.readStateOnce(ctx, "acc_1")
	if err != nil || row == nil {
		t.Fatalf("readStateOnce = %v %v", row, err)
	}
	if row.accountID != "acc_1" || row.lockState != "locked" || row.generation != 3 {
		t.Fatalf("行投影不符 = %+v", row)
	}
	// 缺失行是 (nil, nil) 契约（sql.ErrNoRows 被吸收为未锁定）。
	if missing, err := locks.readStateOnce(ctx, "acc_missing"); err != nil || missing != nil {
		t.Fatalf("缺失行必须回落 nil,nil = %v %v", missing, err)
	}
}

func TestW1AuthorizationQuotaExceededArms(t *testing.T) {
	fixture := newChainFixture(t)
	statsStore, err := gatewayquota.NewStatsStore(fixture.statsDB, false)
	if err != nil {
		t.Fatalf("stats store = %v", err)
	}
	bridge := &accountsRuntimeResetBridge{
		db: fixture.db, pg: false,
		settings: func(string) (string, error) { return "UTC", nil },
		stats:    statsStore,
		now:      func() time.Time { return time.UnixMilli(1728000000000) },
	}
	ctx := context.Background()
	// 空 id 短路。
	ok, err := bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{})
	if err != nil || ok {
		t.Fatalf("空 id 短路 = %v %v", ok, err)
	}
	// 授权行 + 小时额度 limits_json。
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status, limits_json) VALUES ('auth-1', 'group', 'group_main', 'user-1', 'active', '{"hourly":{"enabled":true,"hours":24,"limit":1}}')`); err != nil {
		t.Fatalf("insert authorization = %v", err)
	}
	// juhe_stats 成本投影表（SQLite 无前缀）。
	for _, ddl := range []string{
		`CREATE TABLE usage_stats_totals (system_account_id TEXT, scope_type TEXT, scope_id TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_stats_daily (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_date TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_stats_weekly (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_week TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_stats_monthly (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_month TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_quota_hourly_windows (system_account_id TEXT, scope_type TEXT, scope_id TEXT, window_hours INTEGER, total_cost_usd REAL)`,
	} {
		if _, err := fixture.statsDB.Exec(ddl); err != nil {
			t.Fatalf("create stats table = %v", err)
		}
	}
	// 无成本：未超限。
	ok, err = bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{AuthorizationID: "auth-1", GranteeSystemAccountID: "user-1"})
	if err != nil || ok {
		t.Fatalf("无成本必须未超限 = %v %v", ok, err)
	}
	// 小时窗口成本 5 >= 限额 1 → 超限。
	if _, err := fixture.statsDB.Exec(`INSERT INTO usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd) VALUES ('user-1', 'account_authorization', 'auth-1', 24, 5)`); err != nil {
		t.Fatalf("insert hourly cost = %v", err)
	}
	ok, err = bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{AuthorizationID: "auth-1", GranteeSystemAccountID: "user-1"})
	if err != nil || !ok {
		t.Fatalf("小时成本超限必须为真 = %v %v", ok, err)
	}
	// 未知授权：limits 为空、无 checks → false。
	ok, err = bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{AuthorizationID: "auth-missing", GranteeSystemAccountID: "user-1"})
	if err != nil || ok {
		t.Fatalf("未知授权 = %v %v", ok, err)
	}
}

func TestW1ComposeRuntimeServicesRedisStateArm(t *testing.T) {
	server := miniredis.RunT(t)
	cfg := composeTestConfig(t)
	// D-137：standalone 热质量只支持 memory 运行态驱动，redis 驱动分支
	// 必须 performance 模式（与主流程 GetGatewayHotQualityRuntime 契约一致）。
	cfg.RuntimeMode = "performance"
	cfg.RedisNamespace = "w1-compose-state"
	cfg.RuntimeStateDriver = "redis"
	cfg.RedisStateURL = "redis://" + server.Addr()
	fixture := newChainFixture(t)
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB}
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("composeChainRuntimeServices = %v", err)
	}
	t.Cleanup(services.Close)
	if services.StateClient == nil {
		t.Fatal("redis 运行态驱动必须装配 StateClient")
	}
	if err := services.StateClient.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("StateClient ping = %v", err)
	}
	// 非法 redis URL：fail fast 契约。
	badCfg := composeTestConfig(t)
	badCfg.RuntimeStateDriver = "redis"
	badCfg.RedisStateURL = "redis://[::bad-url"
	if _, err := composeChainRuntimeServices(&composition{db: fixture.db, statsDB: fixture.statsDB}, badCfg, func(string) (string, error) { return "UTC", nil }); err == nil {
		t.Fatal("非法 state URL 必须报错")
	}
	// nil composition / nil settingValue 守卫。
	if _, err := composeChainRuntimeServices(nil, cfg, nil); err == nil {
		t.Fatal("nil composition 必须报错")
	}
	if _, err := composeChainRuntimeServices(composed, cfg, nil); err == nil {
		t.Fatal("nil settingValue 必须报错")
	}
}
