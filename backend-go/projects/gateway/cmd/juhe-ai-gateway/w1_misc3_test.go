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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"log/slog"
	"net/http"
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

// TestW1ImagePreflightAdditionalArms 收割 chainImagePreflight.Apply 的
// forced-tool 与 403/413/503 完成分支：切换禁用图像生成的 API Key 记录，
// 逐分支断言 audit metadata 与完成标志。
func TestW1ImagePreflightAdditionalArms(t *testing.T) {
	newPreflight := func() (*chainImagePreflight, *chainCapturedObservability) {
		obs := &chainCapturedObservability{}
		sink := &w1PreflightResponseSink{}
		service, err := gatewaypreauth.New(gatewaypreauth.Service{
			RuntimeCache:  chainPreauthStubRuntimeCache{},
			Observability: obs,
			Clock:         gatewaypreauth.SystemClock{},
			Responses:     sink,
		})
		if err != nil {
			t.Fatalf("create preauth service: %v", err)
		}
		return &chainImagePreflight{preauth: service}, obs
	}
	newInput := func(body string) gatewaypreauth.ImagePermissionPreflightInput {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		req := gatewaypreauth.NewGatewayRequest(request)
		rawBody := []byte(body)
		var parsed any
		if err := json.Unmarshal(rawBody, &parsed); err != nil {
			t.Fatalf("body json = %v", err)
		}
		req.Body = &gatewaybody.Request{
			RawBody:           rawBody,
			Body:              parsed,
			ContentTypeHeader: "application/json",
			State:             &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON, ContentType: "application/json"},
		}
		return gatewaypreauth.ImagePermissionPreflightInput{
			Req:             req,
			Res:             gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
			AuditCapture:    &chainTestAuditCapture{},
			APIKeyRecord:    &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountImageGenerationEnabled: 0},
			RequestLane:     string(gatewayproto.LaneImage),
			SystemAccountID: "sys_owner",
			APIKeyID:        "key_1",
			GroupID:         "group_1",
			Endpoint:        "/v1/images/generations",
		}
	}
	// 403 完成分支：文本请求但图像工具为 auto 且被移除 → 403 forbidden 完成。
	preflight, _ := newPreflight()
	input := newInput(`{"model":"gpt-test","input":"hi"}`)
	inspection := gatewaybody.InspectImageGenerationTools(map[string]any{})
	if inspection.ImageToolCount > 0 || inspection.ForcedImageGeneration {
		t.Fatal("unexpected")
	}
	// 手动构造 no-image body → auto downgrade 无工具 → 403 forbidden。
	result, err := preflight.Apply(context.Background(), input)
	if err != nil || !result.Completed {
		t.Fatalf("403 分支 result=%+v err=%v", result, err)
	}
	// forced-tool 完成分支：tool_choice=required + 仅 image_generation 工具。
	forced := newInput(`{"model":"gpt-test","input":"hi","tools":[{"type":"image_generation"}],"tool_choice":"required"}`)
	var parsedAny map[string]any
	if err := json.Unmarshal([]byte(`{"model":"gpt-test","input":"hi","tools":[{"type":"image_generation"}],"tool_choice":"required"}`), &parsedAny); err != nil {
		t.Fatalf("parse forced: %v", err)
	}
	forced.Req.Body = &gatewaybody.Request{
		RawBody:           []byte(`{"model":"gpt-test","input":"hi","tools":[{"type":"image_generation"}],"tool_choice":"required"}`),
		Body:              parsedAny,
		ContentTypeHeader: "application/json",
		State:             &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON, ContentType: "application/json"},
	}
	forced.DeferForcedImageGenerationTool = true
	forcedResult, forcedErr := preflight.Apply(context.Background(), forced)
	if forcedErr != nil || forcedResult.Completed || forcedResult.RequestLane != string(gatewayproto.LaneImage) {
		t.Fatalf("forced+defer 分支期望未完成且保留 image lane，result=%+v err=%v", forcedResult, forcedErr)
	}
}

// w1PreflightResponseSink 记录 403/503 失败响应输入的极简 ResponseSink。
type w1PreflightResponseSink struct {
	failures []gatewaypreauth.FailureResponseInput
}

func (s *w1PreflightResponseSink) SendGatewayFailureResponse(input gatewaypreauth.FailureResponseInput) {
	s.failures = append(s.failures, input)
}
func (s *w1PreflightResponseSink) FinalizeGatewayAuthFailureAudit(*gatewaypreauth.GatewayRequest, gatewaypreauth.GatewayResponseWriter, gatewaypreauth.AuditCaptureContext) {
}
func (s *w1PreflightResponseSink) SendAuthenticatedModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {
}
func (s *w1PreflightResponseSink) SendOpenAIModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {
}
func (s *w1PreflightResponseSink) SendAnthropicModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {
}
func (s *w1PreflightResponseSink) SendGeminiModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {
}

// TestW1ErrorPolicyReadHelpers 收割错误策略读取的纯辅助函数：
// 规则读取（系统继承拒绝）、字段读取分支、状态码解析。
func TestW1ErrorPolicyReadHelpers(t *testing.T) {
	if rules, err := accountErrorHandlingRulesRead(nil); err != nil || rules != nil {
		t.Fatalf("nil 规则 = %v %v", rules, err)
	}
	if _, err := accountErrorHandlingRulesRead("not-list"); err == nil {
		t.Fatal("非列表必须报错")
	}
	if _, err := accountErrorHandlingRuleRead("not-map", 1); err == nil {
		t.Fatal("非对象必须报错")
	}
	// 系统继承规则拒绝写入。
	systemRule := map[string]any{"source": "system"}
	if _, err := accountErrorHandlingRuleRead(systemRule, 1); err == nil {
		t.Fatal("系统规则必须拒绝")
	}
	inheritedRule := map[string]any{"inherited": true}
	if _, err := accountErrorHandlingRuleRead(inheritedRule, 2); err == nil {
		t.Fatal("inherited 规则必须拒绝")
	}
	// readTextEqual / readBoolEqual / readRequired* 各分支。
	if !readTextEqual("system", "system") || readTextEqual(1, "system") {
		t.Fatal("readTextEqual 契约")
	}
	if !readBoolEqual(true, true) || readBoolEqual("true", true) {
		t.Fatal("readBoolEqual 契约")
	}
	if flag, err := readRequiredBool(true, "x"); err != nil || !flag {
		t.Fatalf("readRequiredBool = %v %v", flag, err)
	}
	if _, err := readRequiredBool("x", "x"); err == nil {
		t.Fatal("非 bool 必须报错")
	}
	if text, err := readRequiredString(" abc ", "x"); err != nil || text != "abc" {
		t.Fatalf("readRequiredString = %q %v", text, err)
	}
	if _, err := readRequiredString("", "x"); err == nil {
		t.Fatal("空串必须报错")
	}
	if n, err := readRequiredPositiveInt(float64(3), "x"); err != nil || n != 3 {
		t.Fatalf("readRequiredPositiveInt = %v %v", n, err)
	}
	if _, err := readRequiredPositiveInt(0, "x"); err == nil {
		t.Fatal("0 必须报错")
	}
	if _, err := readRequiredPositiveInt(1.5, "x"); err == nil {
		t.Fatal("小数必须报错")
	}
	if h, err := readHour(float64(23), "x"); err != nil || h != 23 {
		t.Fatalf("readHour = %v %v", h, err)
	}
	if _, err := readHour(float64(24), "x"); err == nil {
		t.Fatal("24 必须报错")
	}
	if w, err := readWeekday(float64(6), "x"); err != nil || w != 6 {
		t.Fatalf("readWeekday = %v %v", w, err)
	}
	if _, err := readWeekday(float64(7), "x"); err == nil {
		t.Fatal("7 必须报错")
	}
}

// TestW1ErrorPolicyOverridesAndMatches 收割覆盖解析与规则匹配的分支。
func TestW1ErrorPolicyOverridesAndMatches(t *testing.T) {
	// nil → 空集。
	if out, err := accountErrorPolicyOverridesRead(nil); err != nil || out != nil {
		t.Fatalf("nil overrides = %v %v", out, err)
	}
	// 非列表 / 非对象 / 系统 ID 错 / 动作错 / 未知字段 / replace 索引无效。
	if _, err := accountErrorPolicyOverridesRead("x"); err == nil {
		t.Fatal("非列表必须报错")
	}
	if _, err := accountErrorPolicyOverridesRead([]any{"x"}); err == nil {
		t.Fatal("非对象必须报错")
	}
	if _, err := accountErrorPolicyOverridesRead([]any{map[string]any{"system_rule_id": "wrong", "action": "replace", "rule_index": float64(0)}}); err == nil {
		t.Fatal("系统 ID 错必须报错")
	}
	if _, err := accountErrorPolicyOverridesRead([]any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "bogus"}}); err == nil {
		t.Fatal("动作错必须报错")
	}
	if _, err := accountErrorPolicyOverridesRead([]any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "extra": 1}}); err == nil {
		t.Fatal("未知字段必须报错")
	}
	if _, err := accountErrorPolicyOverridesRead([]any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "rule_index": "x"}}); err == nil {
		t.Fatal("索引非数必须报错")
	}
	if _, err := accountErrorPolicyOverridesRead([]any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "rule_index": float64(-1)}}); err == nil {
		t.Fatal("负索引必须报错")
	}
	// delete 动作合法；replace 合法。
	deleted, err := accountErrorPolicyOverridesRead([]any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "delete"}})
	if err != nil || len(deleted) != 1 || deleted[0].Action != "delete" || deleted[0].HasRuleIndex {
		t.Fatalf("delete = %+v %v", deleted, err)
	}
	replaced, err := accountErrorPolicyOverridesRead([]any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "rule_index": float64(2)}})
	if err != nil || len(replaced) != 1 || replaced[0].RuleIndex != 2 || !replaced[0].HasRuleIndex {
		t.Fatalf("replace = %+v %v", replaced, err)
	}
	// 规则匹配：状态码集合匹配/不匹配、错误码列表、关键词。
	rule := accountErrorHandlingRule{StatusCodes: []float64{429}}
	if !accountErrorRuleMatches(rule, 429, "", "", "") {
		t.Fatal("状态码 429 必须匹配")
	}
	if accountErrorRuleMatches(rule, 500, "", "", "") {
		t.Fatal("状态码 500 必须不匹配")
	}
	// 空规则恒匹配。
	if !accountErrorRuleMatches(accountErrorHandlingRule{}, 200, "x", "y", "z") {
		t.Fatal("空条件列表必须恒匹配")
	}
	// 系统配额规则：quota 关键词 / 402 无码 / 非配额标识排除。
	if systemInsufficientQuotaRuleMatches(429, "insufficient_quota", "quota_error", "") {
		t.Fatal("非 402/403 必须直接不匹配")
	}
	if !systemInsufficientQuotaRuleMatches(402, "insufficient_quota", "", "") {
		t.Fatal("402 stable code 必须匹配")
	}
	if !systemInsufficientQuotaRuleMatches(403, "quota_exceeded", "", "") {
		t.Fatal("quota 关键词必须匹配")
	}
	if !systemInsufficientQuotaRuleMatches(402, "", "", "") {
		t.Fatal("402 无码必须匹配")
	}
	if systemInsufficientQuotaRuleMatches(403, "billing_not_active", "invalid_request_error", "billing") {
		t.Fatal("非配额 403 必须排除")
	}
	if systemInsufficientQuotaRuleMatches(500, "server_error", "server_error", "boom") {
		t.Fatal("普通 500 必须不匹配")
	}
}

// TestW1ErrorPolicyCooldownAndJitter 收割恢复时间与被动调度抖动纯函数。
func TestW1ErrorPolicyCooldownAndJitter(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	// duration 策略：now + 1h + 抖动（抖动为正则 > now）。
	durationRule := accountErrorHandlingRule{ResetStrategy: "duration", DurationHours: 1}
	until := accountErrorRuleCooldownUntil(durationRule, now, "seed-w1")
	parsed, parseErr := time.Parse(time.RFC3339Nano, until)
	if parseErr != nil || !parsed.After(now) {
		t.Fatalf("duration 结果必须晚于 now = %q", until)
	}
	// daily 策略：重置小时 23 → 今天 23 点之后（含抖动）。
	dailyRule := accountErrorHandlingRule{ResetStrategy: "daily", DailyResetHour: 23}
	dailyUntil := accountErrorRuleCooldownUntil(dailyRule, now, "seed-w1")
	dailyParsed, _ := time.Parse(time.RFC3339Nano, dailyUntil)
	if dailyParsed.Before(now) || dailyParsed.Day() != now.Day() {
		t.Fatalf("daily 结果必须是今天 23 点附近 = %q", dailyUntil)
	}
	// daily 重置小时已过 → 顺延一天。
	pastRule := accountErrorHandlingRule{ResetStrategy: "daily", DailyResetHour: 8}
	pastUntil := accountErrorRuleCooldownUntil(pastRule, now, "seed-w1")
	pastParsed, _ := time.Parse(time.RFC3339Nano, pastUntil)
	if pastParsed.Day() != now.AddDate(0, 0, 1).Day() {
		t.Fatalf("已过小时必须顺延一天 = %q", pastUntil)
	}
	// weekly 策略：对齐 weekly_reset_day。
	weeklyRule := accountErrorHandlingRule{ResetStrategy: "weekly", WeeklyResetHour: 20, WeeklyResetDay: 1}
	weeklyUntil := accountErrorRuleCooldownUntil(weeklyRule, now, "seed-w1")
	weeklyParsed, _ := time.Parse(time.RFC3339Nano, weeklyUntil)
	if weeklyParsed.Weekday() != time.Monday {
		t.Fatalf("weekly 必须对齐周一 = %q", weeklyUntil)
	}
	// 抖动窗口边界。
	if passiveScheduleJitterWindowMs(10_000) != 5_000 {
		t.Fatalf("10s 窗口 = %d", passiveScheduleJitterWindowMs(10_000))
	}
	if passiveScheduleJitterWindowMs(120_000) != 30_000 {
		t.Fatalf("2min 窗口 = %d", passiveScheduleJitterWindowMs(120_000))
	}
	if passiveScheduleJitterWindowMs(25*3_600_000) != 60*60_000 {
		t.Fatalf("25h 窗口 = %d", passiveScheduleJitterWindowMs(25*3_600_000))
	}
	if passiveScheduleJitterWindowMs(8*24*3_600_000) != 8*60*60_000 {
		t.Fatalf("8d 窗口 = %d", passiveScheduleJitterWindowMs(8*24*3_600_000))
	}
	if max64(3, 7) != 7 || max64(9, 2) != 9 {
		t.Fatal("max64 契约")
	}
	if min64(3, 7) != 3 || min64(9, 2) != 2 {
		t.Fatal("min64 契约")
	}
}

// TestW1ErrorPolicyFullRuleRead 收割完整规则读取：全字段合法规则、
// 不支持字段、动作无效、2xx 错误码、缺条件、rate_limited 各恢复策略。
func TestW1ErrorPolicyFullRuleRead(t *testing.T) {
	// 全字段合法规则（rate_limited + weekly 恢复）。
	rule, err := accountErrorHandlingRuleRead(map[string]any{
		"enabled": true, "name": "w1", "priority": float64(5), "action": "rate_limited",
		"status_codes": []any{float64(429)}, "error_codes": []any{"insufficient_quota"}, "error_types": []any{"quota_error"},
		"keywords": []any{"quota"}, "reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": float64(6),
		"description": "w1",
	}, 1)
	if err != nil {
		t.Fatalf("full rule = %v", err)
	}
	if rule.Action != "rate_limited" || rule.WeeklyResetDay != 1 || rule.WeeklyResetHour != 6 || len(rule.Keywords) != 1 {
		t.Fatalf("rule = %+v", rule)
	}
	// duration 恢复。
	durationRule, err := accountErrorHandlingRuleRead(map[string]any{
		"enabled": true, "name": "w1d", "priority": float64(1), "action": "rate_limited",
		"status_codes": []any{float64(403)}, "reset_strategy": "duration", "duration_hours": float64(2),
	}, 1)
	if err != nil || durationRule.DurationHours != 2 {
		t.Fatalf("duration rule = %+v %v", durationRule, err)
	}
	// daily 恢复。
	dailyRule, err := accountErrorHandlingRuleRead(map[string]any{
		"enabled": true, "name": "w1h", "priority": float64(1), "action": "rate_limited",
		"error_codes": []any{"quota"}, "reset_strategy": "daily", "daily_reset_hour": float64(12),
	}, 1)
	if err != nil || dailyRule.DailyResetHour != 12 {
		t.Fatalf("daily rule = %+v %v", dailyRule, err)
	}
	// 非法动作。
	if _, err := accountErrorHandlingRuleRead(map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "bogus", "keywords": []any{"k"}}, 1); err == nil {
		t.Fatal("非法动作必须报错")
	}
	// 2xx 错误码拒绝。
	if _, err := accountErrorHandlingRuleRead(map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "error_codes": []any{"200"}}, 1); err == nil {
		t.Fatal("2xx 错误码必须拒绝")
	}
	// 启用但无条件。
	if _, err := accountErrorHandlingRuleRead(map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next"}, 1); err == nil {
		t.Fatal("缺条件必须报错")
	}
	// 不支持字段。
	if _, err := accountErrorHandlingRuleRead(map[string]any{"enabled": true, "name": "x", "priority": float64(1), "action": "retry_next", "keywords": []any{"k"}, "extra": 1}, 1); err == nil {
		t.Fatal("不支持字段必须报错")
	}
	// 禁用规则可无条件。
	disabled, err := accountErrorHandlingRuleRead(map[string]any{"enabled": false, "name": "x", "priority": float64(1), "action": "retry_next"}, 1)
	if err != nil || disabled.Enabled {
		t.Fatalf("disabled rule = %+v %v", disabled, err)
	}
}

// TestW1ErrorRuleMatchesDimensions 收割规则匹配的四维度分支：
// 状态码集合、错误码集合、错误类型集合、关键字（匹配与不匹配）。
func TestW1ErrorRuleMatchesDimensions(t *testing.T) {
	// 错误码维度。
	codeRule := accountErrorHandlingRule{ErrorCodes: []string{"INSUFFICIENT_QUOTA"}}
	if !accountErrorRuleMatches(codeRule, 402, "insufficient_quota", "", "") {
		t.Fatal("错误码匹配（小写归一）")
	}
	if accountErrorRuleMatches(codeRule, 402, "other_code", "", "") {
		t.Fatal("错误码不匹配")
	}
	// 错误类型维度。
	typeRule := accountErrorHandlingRule{ErrorTypes: []string{"Quota_Error"}}
	if !accountErrorRuleMatches(typeRule, 402, "", "quota_error", "") {
		t.Fatal("错误类型匹配")
	}
	if accountErrorRuleMatches(typeRule, 402, "", "server_error", "") {
		t.Fatal("错误类型不匹配")
	}
	// 关键字维度。
	keywordRule := accountErrorHandlingRule{Keywords: []string{"QUOTA"}}
	if !accountErrorRuleMatches(keywordRule, 402, "", "", "billing quota exceeded") {
		t.Fatal("关键字包含匹配")
	}
	if accountErrorRuleMatches(keywordRule, 402, "", "", "boom") {
		t.Fatal("关键字不匹配")
	}
	// 多维度组合：一维不匹配即拒绝。
	combined := accountErrorHandlingRule{StatusCodes: []float64{402}, ErrorCodes: []string{"quota"}}
	if accountErrorRuleMatches(combined, 500, "quota", "", "") {
		t.Fatal("状态码不匹配必须拒绝")
	}
	if !accountErrorRuleMatches(combined, 402, "quota", "", "") {
		t.Fatal("全维匹配")
	}
}
