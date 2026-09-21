package main

// w1 波次收尾（dispatch tail）：以 %TEMP%/w1pkg-unit-final-all.out 的 set 模式
// 覆盖率解析为准，补齐五个目标文件仍未命中的分支。本文件只新增 w1x 前缀标识符
// 与 TestW1X 测试，复用既有 fixture（newAccountLocksFixture / seedChainSelectorDB /
// w1nDropTables / w1rAuditCapture / strPtr），不修改任何现有文件。
//
// 覆盖面：
//   chain_account_locks.go  findState/ListStates/observation/四操作错误臂
//   chain_accounts.go       资源访问器、可用性门、resolveGroupAccess、授权读取、
//                           access 解析、eligibility 错误臂、排序尾巴
//   chain_dispatch.go       Async 适配器剩余臂、configured-policy preflight 503、
//                           list-availability 脏标记（sqlite ATTACH 模拟 PG 方言）
//   chain_wiring_w2c.go     binding 行回退投影、并发槽错误臂、电路服务 fork、
//                           suppression lease/result/waiter 臂、quota db-service 桥
//   chain_runtime.go        ipstatsTimezone、composeChainRuntimeServices 前置错误臂

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// 局部 helper（全部 w1x 前缀）
// ---------------------------------------------------------------------------

func w1xInt64Ptr(value int64) *int64 { return &value }

func w1xEnvOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// w1xOpenBareSQLite 打开一个空业务库句柄（无任何业务表），用于确定性的
// “表缺失 -> SQL 错误”错误臂与最小组合根构造。
func w1xOpenBareSQLite(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open sqlite %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w1xRouteCoordinator 是 gatewayrouting.GatewayRouteCoordinatorOwner 的记录用
// fake：CompleteFailure 可注入错误。
type w1xRouteCoordinator struct {
	failures []gatewayrouting.GatewayRouteFinalFailure
	err      error
}

func (c *w1xRouteCoordinator) RequestFallback(context.Context, string) (gatewayrouting.GatewayRouteFallbackDecision, error) {
	return gatewayrouting.GatewayRouteFallbackDecision{}, nil
}

func (c *w1xRouteCoordinator) CompleteFailure(_ context.Context, failure gatewayrouting.GatewayRouteFinalFailure) error {
	c.failures = append(c.failures, failure)
	return c.err
}

// w1xFailingTurnStore 是 gatewaycodex.TurnRetryStateStore 的恒错 fake。
type w1xFailingTurnStore struct{}

func (w1xFailingTurnStore) GetJSON(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("w1x 状态存储读取失败")
}

func (w1xFailingTurnStore) CompareSetJSON(context.Context, string, json.RawMessage, any, int64) (bool, error) {
	return false, errors.New("w1x 状态存储写入失败")
}

func (w1xFailingTurnStore) Incr(context.Context, string, int64) (int64, error) {
	return 0, errors.New("w1x 状态存储自增失败")
}

// w1xSeedLockRowDeadOnly 在去掉 accounts 表的 fixture 上种一行 DEAD_CONFIRMED
// 锁状态：findState 随后的 accounts 读必然失败，用于注入恢复路径错误臂。
func w1xSeedLockRowDeadOnly(t *testing.T, f *lockFixture, accountID string) {
	t.Helper()
	w1nDropTables(t, f.db, "accounts")
	f.seedLockRow(t, chainAccountLockRow{accountID: accountID, enabled: 1, lockState: "DEAD_CONFIRMED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID: sql.NullString{String: accountID + ":1:t", Valid: true}})
}

// ---------------------------------------------------------------------------
// 1. chain_account_locks.go
// ---------------------------------------------------------------------------

func TestW1XFindStateBlankAndRecoveryErrorArms(t *testing.T) {
	ctx := context.Background()

	// 空白账户 ID：与「未锁」等价，直接 (nil, nil)。
	fixture := newAccountLocksFixture(t)
	row, err := fixture.locks.findState(ctx, "   ")
	if row != nil || err != nil {
		t.Fatalf("findState(空白) = (%+v, %v), want (nil, nil)", row, err)
	}

	// DEAD_CONFIRMED 且 accounts 行缺失：保持行原样返回（不误判为错误）。
	missing := newAccountLocksFixture(t)
	missing.seedLockRow(t, chainAccountLockRow{accountID: "w1x-dead", enabled: 1, lockState: "DEAD_CONFIRMED",
		deathTimeout: 300, retryInterval: 5, generation: 2})
	row, err = missing.locks.findState(ctx, "w1x-dead")
	if err != nil {
		t.Fatalf("findState(DEAD_CONFIRMED, accounts 缺行): %v", err)
	}
	if row == nil || row.lockState != "DEAD_CONFIRMED" || row.generation != 2 {
		t.Fatalf("findState 应原样返回 DEAD_CONFIRMED 行: %+v", row)
	}

	// accounts 表整体缺失：恢复探测 SELECT 报错必须向上透传。
	broken := newAccountLocksFixture(t)
	w1xSeedLockRowDeadOnly(t, broken, "w1x-dead")
	row, err = broken.locks.findState(ctx, "w1x-dead")
	if err == nil || row != nil {
		t.Fatalf("findState(accounts 表缺失) = (%+v, %v), want (nil, err)", row, err)
	}
}

func TestW1XListStatesAsyncArms(t *testing.T) {
	ctx := context.Background()

	fixture := newAccountLocksFixture(t)
	fixture.seedAccount(t, "w1x-live", "active", 1, "")
	fixture.seedLockRow(t, chainAccountLockRow{accountID: "w1x-live", enabled: 1, lockState: "LOCKED_IDLE",
		deathTimeout: 300, retryInterval: 5, generation: 3})
	states, err := fixture.locks.ListStatesAsync(ctx, []string{"   ", "w1x-missing", "w1x-live"})
	if err != nil {
		t.Fatalf("ListStatesAsync: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("ListStatesAsync = %v, want 仅 w1x-live", states)
	}
	if view, ok := states["w1x-live"]; !ok || view.Generation != 3 {
		t.Fatalf("w1x-live 投影 = %+v", view)
	}

	// findState 错误必须中断批量读取。
	broken := newAccountLocksFixture(t)
	w1xSeedLockRowDeadOnly(t, broken, "w1x-dead")
	states, err = broken.locks.ListStatesAsync(ctx, []string{"w1x-dead"})
	if err == nil || states != nil {
		t.Fatalf("ListStatesAsync(findState 错误) = (%+v, %v), want (nil, err)", states, err)
	}
}

func TestW1XObservationMatchesRowFullTable(t *testing.T) {
	expired := sql.NullString{String: isoMillisOf(time.UnixMilli(1000)), Valid: true}
	future := sql.NullString{String: isoMillisOf(time.UnixMilli(9_000)), Valid: true}
	base := struct {
		state    string
		deadline sql.NullString
		gen      int64
		incident sql.NullString
		lease    sql.NullString
	}{"ENGAGED", expired, 5, sql.NullString{String: "inc-5", Valid: true}, sql.NullString{String: "L1", Valid: true}}

	cases := []struct {
		name string
		mut  func(*struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		})
		observation *gatewaydispatch.AccountLockObservation
		want        bool
	}{
		{"非 ENGAGED", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
			b.state = "DEAD_CONFIRMED"
		}, nil, false},
		{"无 deadline", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
			b.deadline = sql.NullString{}
		}, nil, false},
		{"deadline 未到期", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
			b.deadline = future
		}, nil, false},
		{"nil 观察放行", func(_ *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
		}, nil, true},
		{"代际不匹配", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
		}, &gatewaydispatch.AccountLockObservation{Generation: 4, IncidentID: "inc-5"}, false},
		{"事故不匹配", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
		}, &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "inc-x"}, false},
		{"空 lease 观察遇持约行", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
		}, &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "inc-5", LeaseID: fenceLease("")}, false},
		{"空 lease 观察遇 NULL lease 行", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
			b.lease = sql.NullString{}
		}, &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "inc-5", LeaseID: fenceLease("")}, true},
		{"lease 不匹配", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
		}, &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "inc-5", LeaseID: fenceLease("L2")}, false},
		{"lease 匹配", func(b *struct {
			state    string
			deadline sql.NullString
			gen      int64
			incident sql.NullString
			lease    sql.NullString
		}) {
		}, &gatewaydispatch.AccountLockObservation{Generation: 5, IncidentID: "inc-5", LeaseID: fenceLease("L1")}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			current := base
			if testCase.mut != nil {
				testCase.mut(&current)
			}
			got := chainAccountLockObservationMatchesRow(current.state, current.deadline, current.gen,
				current.incident, current.lease, testCase.observation, 5000)
			if got != testCase.want {
				t.Fatalf("observationMatchesRow = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestW1XRecordFailureBlankAndErrorArms(t *testing.T) {
	ctx := context.Background()

	fixture := newAccountLocksFixture(t)
	if err := fixture.locks.RecordFailureAsync(ctx, "  ", "reason", nil); err != nil {
		t.Fatalf("RecordFailureAsync(空白) = %v, want nil", err)
	}

	// LOCKED_IDLE 行在但 accounts 表缺失：状态读取后的账户状态 SELECT 失败。
	brokenStatus := newAccountLocksFixture(t)
	w1nDropTables(t, brokenStatus.db, "accounts")
	brokenStatus.seedLockRow(t, chainAccountLockRow{accountID: "w1x-idle", enabled: 1, lockState: "LOCKED_IDLE",
		deathTimeout: 300, retryInterval: 5, generation: 1})
	if err := brokenStatus.locks.RecordFailureAsync(ctx, "w1x-idle", "reason", nil); err == nil {
		t.Fatal("accounts 表缺失时 RecordFailureAsync 必须报错")
	}

	// DEAD_CONFIRMED 行：findState 恢复探测失败先行中断。
	brokenFind := newAccountLocksFixture(t)
	w1xSeedLockRowDeadOnly(t, brokenFind, "w1x-dead")
	if err := brokenFind.locks.RecordFailureAsync(ctx, "w1x-dead", "reason", nil); err == nil {
		t.Fatal("findState 错误必须向上透传")
	}
}

func TestW1XSettleDeadlineBlankFindStateAndAccountErrorArms(t *testing.T) {
	ctx := context.Background()
	nowMs := newLockClock().NowMs()

	fixture := newAccountLocksFixture(t)
	if err := fixture.locks.SettleDeadlineAsync(ctx, "  ", nowMs, nil); err != nil {
		t.Fatalf("SettleDeadlineAsync(空白) = %v, want nil", err)
	}

	// findState 错误透传。
	brokenFind := newAccountLocksFixture(t)
	w1xSeedLockRowDeadOnly(t, brokenFind, "w1x-dead")
	if err := brokenFind.locks.SettleDeadlineAsync(ctx, "w1x-dead", nowMs, nil); err == nil {
		t.Fatal("settle: findState 错误必须向上透传")
	}

	// original_status 为 NULL 且 accounts 表缺失：回退读失败。
	nullStatus := newAccountLocksFixture(t)
	w1nDropTables(t, nullStatus.db, "accounts")
	nullStatus.seedLockRow(t, chainAccountLockRow{accountID: "w1x-null", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID: sql.NullString{String: "w1x-null:1:t", Valid: true},
		deadlineAt: sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs - 1)), Valid: true}})
	if err := nullStatus.locks.SettleDeadlineAsync(ctx, "w1x-null", nowMs, nil); err == nil {
		t.Fatal("settle: original_status 回退读失败必须报错")
	}

	// original_status = active 且 accounts 表缺失：账户行降级 UPDATE 失败。
	activeStatus := newAccountLocksFixture(t)
	w1nDropTables(t, activeStatus.db, "accounts")
	activeStatus.seedLockRow(t, chainAccountLockRow{accountID: "w1x-active", enabled: 1, lockState: "ENGAGED",
		deathTimeout: 300, retryInterval: 5, generation: 1,
		incidentID:     sql.NullString{String: "w1x-active:1:t", Valid: true},
		deadlineAt:     sql.NullString{String: isoMillisOf(time.UnixMilli(nowMs - 1)), Valid: true},
		originalStatus: sql.NullString{String: "active", Valid: true}})
	if err := activeStatus.locks.SettleDeadlineAsync(ctx, "w1x-active", nowMs, nil); err == nil {
		t.Fatal("settle: 账户降级 UPDATE 失败必须报错")
	}
}

func TestW1XLeaseFamilyBlankAndFindStateErrorArms(t *testing.T) {
	ctx := context.Background()
	nowMs := newLockClock().NowMs()

	fixture := newAccountLocksFixture(t)
	if lease, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "  ", 3_000); err != nil || !lease.Allowed {
		t.Fatalf("Acquire(空白) = (%+v, %v), want 放行", lease, err)
	}
	if consumed, err := fixture.locks.ConsumeRetryLeaseAsync(ctx, "w1x-a", "  "); err != nil || consumed {
		t.Fatalf("Consume(空白租约) = (%v, %v), want (false, nil)", consumed, err)
	}
	if released, err := fixture.locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{LeaseID: "  "}); err != nil || !released {
		t.Fatalf("Release(空白租约) = (%v, %v), want (true, nil)", released, err)
	}

	for _, accountID := range []string{"acquire", "consume", "release"} {
		broken := newAccountLocksFixture(t)
		w1xSeedLockRowDeadOnly(t, broken, "w1x-dead")
		switch accountID {
		case "acquire":
			if _, err := broken.locks.AcquireRetryLeaseAsync(ctx, "w1x-dead", 3_000); err == nil {
				t.Fatal("acquire: findState 错误必须向上透传")
			}
		case "consume":
			if _, err := broken.locks.ConsumeRetryLeaseAsync(ctx, "w1x-dead", "lease"); err == nil {
				t.Fatal("consume: findState 错误必须向上透传")
			}
		case "release":
			if _, err := broken.locks.ReleaseRetryLeaseAsync(ctx, gatewaydispatch.ReleaseRetryLeaseInput{
				AccountID: "w1x-dead", LeaseID: "lease"}); err == nil {
				t.Fatal("release: findState 错误必须向上透传")
			}
		}
	}
	_ = nowMs
}

// ---------------------------------------------------------------------------
// 2. chain_accounts.go
// ---------------------------------------------------------------------------

func TestW1XSelectorResourceAccessorsTable(t *testing.T) {
	row := &chainCandidateRow{
		ID:                           "w1x-row",
		ProviderCode:                 "openai",
		ProtocolCode:                 sql.NullString{String: "chat", Valid: true},
		ProtocolVersion:              sql.NullString{String: "v1", Valid: true},
		Type:                         "api_key",
		CredentialsEncrypted:         sql.NullString{String: "creds", Valid: true},
		ConcurrencyLimit:             7,
		ProxyProfileID:               sql.NullString{String: "px-1", Valid: true},
		ResourceAccountID:            sql.NullString{String: "w1x-res", Valid: true},
		ResourceProviderCode:         sql.NullString{String: "openai-res", Valid: true},
		ResourceProtocolCode:         sql.NullString{String: "responses", Valid: true},
		ResourceProtocolVersion:      sql.NullString{String: "v2", Valid: true},
		ResourceType:                 sql.NullString{String: "oauth", Valid: true},
		ResourceCredentialsEncrypted: sql.NullString{String: "res-creds", Valid: true},
		ResourceProxyProfileID:       sql.NullString{String: "px-res", Valid: true},
		ResourceConcurrencyLimit:     sql.NullInt64{Int64: 3, Valid: true},
	}
	if got := row.resourceAccountID(); got != "w1x-res" {
		t.Fatalf("resourceAccountID = %q", got)
	}
	if got := row.resourceProxyProfileID(); got != "px-res" {
		t.Fatalf("resourceProxyProfileID = %q", got)
	}
	if got := row.resourceProviderCode(); got != "openai-res" {
		t.Fatalf("resourceProviderCode = %q", got)
	}
	if got := row.resourceProtocolCode(); got != "responses" {
		t.Fatalf("resourceProtocolCode = %q", got)
	}
	if got := row.resourceProtocolVersion(); got != "v2" {
		t.Fatalf("resourceProtocolVersion = %q", got)
	}
	if got := row.resourceType(); got != "oauth" {
		t.Fatalf("resourceType = %q", got)
	}
	if got := row.resourceCredentialsEncrypted(); got != "res-creds" {
		t.Fatalf("resourceCredentialsEncrypted = %q", got)
	}
	if got := row.resourceConcurrencyLimit(); got != 3 {
		t.Fatalf("resourceConcurrencyLimit = %d", got)
	}

	// 资源列缺失时逐列回退到物理账户值。
	fallback := &chainCandidateRow{ID: "w1x-fb", ProviderCode: "openai", Type: "api_key", ConcurrencyLimit: 9,
		ProtocolCode:         sql.NullString{String: "chat", Valid: true},
		ProtocolVersion:      sql.NullString{String: "v1", Valid: true},
		CredentialsEncrypted: sql.NullString{String: "creds", Valid: true},
		ProxyProfileID:       sql.NullString{String: "px-1", Valid: true}}
	if got := fallback.resourceAccountID(); got != "w1x-fb" {
		t.Fatalf("回退 resourceAccountID = %q", got)
	}
	if got := fallback.resourceProxyProfileID(); got != "px-1" {
		t.Fatalf("回退 resourceProxyProfileID = %q", got)
	}
	if got := fallback.resourceProviderCode(); got != "openai" {
		t.Fatalf("回退 resourceProviderCode = %q", got)
	}
	if got := fallback.resourceProtocolCode(); got != "chat" {
		t.Fatalf("回退 resourceProtocolCode = %q", got)
	}
	if got := fallback.resourceProtocolVersion(); got != "v1" {
		t.Fatalf("回退 resourceProtocolVersion = %q", got)
	}
	if got := fallback.resourceType(); got != "api_key" {
		t.Fatalf("回退 resourceType = %q", got)
	}
	if got := fallback.resourceCredentialsEncrypted(); got != "creds" {
		t.Fatalf("回退 resourceCredentialsEncrypted = %q", got)
	}
	if got := fallback.resourceConcurrencyLimit(); got != 9 {
		t.Fatalf("回退 resourceConcurrencyLimit = %d", got)
	}
	// ProxyProfileID 也缺失时空串兜底。
	if got := (&chainCandidateRow{ID: "w1x-none"}).resourceProxyProfileID(); got != "" {
		t.Fatalf("全缺失 resourceProxyProfileID = %q", got)
	}
}

func TestW1XAccountAvailabilityGatesArms(t *testing.T) {
	nowMillis := int64(1_767_225_600_000)
	past := "2025-06-01T00:00:00.000Z"
	future := "2999-01-01T00:00:00.000Z"

	// 非法当前时间：直接报错。
	if _, err := chainAccountAvailableForSelection(&chainCandidateRow{}, "bad-time", false); err == nil {
		t.Fatal("非法 now 必须报错")
	}

	physicalCases := []struct {
		name string
		row  chainCandidateRow
		want bool
	}{
		{"已过期", chainCandidateRow{AccountExpiresAt: sql.NullString{String: past, Valid: true}, Schedulable: 1, Status: "active"}, false},
		{"不可调度", chainCandidateRow{Schedulable: 0, Status: "active"}, false},
		{"非 active", chainCandidateRow{Schedulable: 1, Status: "rate_limited"}, false},
		{"冷却未到期", chainCandidateRow{Schedulable: 1, Status: "active",
			CooldownUntil: sql.NullString{String: future, Valid: true}}, false},
		{"冷却已过", chainCandidateRow{Schedulable: 1, Status: "active",
			CooldownUntil: sql.NullString{String: past, Valid: true}}, true},
		{"无冷却", chainCandidateRow{Schedulable: 1, Status: "active"}, true},
	}
	for _, testCase := range physicalCases {
		t.Run("物理门/"+testCase.name, func(t *testing.T) {
			got, err := chainPhysicalAccountAvailable(&testCase.row, nowMillis, false)
			if err != nil || got != testCase.want {
				t.Fatalf("physical = (%v, %v), want (%v, nil)", got, err, testCase.want)
			}
		})
	}
	if _, err := chainPhysicalAccountAvailable(&chainCandidateRow{Schedulable: 1, Status: "active",
		CooldownUntil: sql.NullString{String: "bad-date", Valid: true}}, nowMillis, false); err == nil {
		t.Fatal("非法冷却时间必须报错")
	}
	// includeUnavailable 放行三种运行态。
	for _, status := range []string{"active", "rate_limited", "temporary_unavailable"} {
		if got, err := chainPhysicalAccountAvailable(&chainCandidateRow{Schedulable: 1, Status: status}, nowMillis, true); err != nil || !got {
			t.Fatalf("includeUnavailable 下 %s 应可用: (%v, %v)", status, got, err)
		}
	}
	if got, err := chainPhysicalAccountAvailable(&chainCandidateRow{Schedulable: 1, Status: "disabled"}, nowMillis, true); err != nil || got {
		t.Fatalf("includeUnavailable 下 disabled 仍应拒绝: (%v, %v)", got, err)
	}

	resourceCases := []struct {
		name           string
		row            chainCandidateRow
		includePending bool
		want           bool
		wantErr        bool
	}{
		{"无授权实例直通", chainCandidateRow{}, false, true, false},
		{"缺资源账户或状态", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true}}, false, false, false},
		{"资源已过期", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:        sql.NullString{String: "src", Valid: true},
			ResourceStatus:           sql.NullString{String: "active", Valid: true},
			ResourceAccountExpiresAt: sql.NullString{String: past, Valid: true}}, false, false, false},
		{"资源过期时间非法", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:        sql.NullString{String: "src", Valid: true},
			ResourceStatus:           sql.NullString{String: "active", Valid: true},
			ResourceAccountExpiresAt: sql.NullString{String: "bad-date", Valid: true}}, false, false, true},
		{"资源标记过期错误", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:     sql.NullString{String: "src", Valid: true},
			ResourceStatus:        sql.NullString{String: "active", Valid: true},
			ResourceSchedulable:   sql.NullInt64{Int64: 1, Valid: true},
			ResourceLastErrorCode: sql.NullString{String: "account_expired", Valid: true}}, false, false, false},
		{"资源不可调度", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:   sql.NullString{String: "src", Valid: true},
			ResourceStatus:      sql.NullString{String: "active", Valid: true},
			ResourceSchedulable: sql.NullInt64{Int64: 0, Valid: true}}, false, false, false},
		{"资源非 active", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:   sql.NullString{String: "src", Valid: true},
			ResourceStatus:      sql.NullString{String: "rate_limited", Valid: true},
			ResourceSchedulable: sql.NullInt64{Int64: 1, Valid: true}}, false, false, false},
		{"资源无冷却", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:   sql.NullString{String: "src", Valid: true},
			ResourceStatus:      sql.NullString{String: "active", Valid: true},
			ResourceSchedulable: sql.NullInt64{Int64: 1, Valid: true}}, false, true, false},
		{"资源冷却已过", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:     sql.NullString{String: "src", Valid: true},
			ResourceStatus:        sql.NullString{String: "active", Valid: true},
			ResourceSchedulable:   sql.NullInt64{Int64: 1, Valid: true},
			ResourceCooldownUntil: sql.NullString{String: past, Valid: true}}, false, true, false},
		{"资源冷却未到", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:     sql.NullString{String: "src", Valid: true},
			ResourceStatus:        sql.NullString{String: "active", Valid: true},
			ResourceSchedulable:   sql.NullInt64{Int64: 1, Valid: true},
			ResourceCooldownUntil: sql.NullString{String: future, Valid: true}}, false, false, false},
		{"资源冷却非法", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:     sql.NullString{String: "src", Valid: true},
			ResourceStatus:        sql.NullString{String: "active", Valid: true},
			ResourceSchedulable:   sql.NullInt64{Int64: 1, Valid: true},
			ResourceCooldownUntil: sql.NullString{String: "bad-date", Valid: true}}, false, false, true},
		{"宽限面 rate_limited", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:   sql.NullString{String: "src", Valid: true},
			ResourceStatus:      sql.NullString{String: "rate_limited", Valid: true},
			ResourceSchedulable: sql.NullInt64{Int64: 1, Valid: true}}, true, true, false},
		{"宽限面 temporary_unavailable", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:   sql.NullString{String: "src", Valid: true},
			ResourceStatus:      sql.NullString{String: "temporary_unavailable", Valid: true},
			ResourceSchedulable: sql.NullInt64{Int64: 1, Valid: true}}, true, true, false},
		{"宽限面拒绝 disabled", chainCandidateRow{AuthorizationInstanceAuthorizationID: sql.NullString{String: "a1", Valid: true},
			ResourceAccountID:   sql.NullString{String: "src", Valid: true},
			ResourceStatus:      sql.NullString{String: "disabled", Valid: true},
			ResourceSchedulable: sql.NullInt64{Int64: 1, Valid: true}}, true, false, false},
	}
	for _, testCase := range resourceCases {
		t.Run("资源门/"+testCase.name, func(t *testing.T) {
			got, err := chainResourceAccountAvailable(&testCase.row, nowMillis, testCase.includePending)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("资源门应报错")
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("resource = (%v, %v), want (%v, nil)", got, err, testCase.want)
			}
		})
	}
}

// w1xSelectorAuthzFixture 建一个带 groups / resource_authorizations /
// group_authorization_settings 的最小选择器库。
func w1xSelectorAuthzFixture(t *testing.T) (*chainAccountsSelector, *sql.DB) {
	t.Helper()
	db := w1xOpenBareSQLite(t, "w1x-authz.sqlite3")
	for _, statement := range []string{
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT, provider_code TEXT, enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT,
			resource_owner_system_account_id TEXT, grantee_system_account_id TEXT,
			status TEXT, expires_at TEXT, effective_source_type TEXT, effective_source_team_id TEXT, limits_json TEXT)`,
		`CREATE TABLE group_authorization_settings (
			authorization_id TEXT, system_account_id TEXT, group_id TEXT,
			enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`,
		`INSERT INTO groups VALUES ('w1x-grp', 'sys_owner', 'openai', 1, 'personal', NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed authz schema: %v", err)
		}
	}
	selector, err := newChainAccountsSelector(db, false, "w1x-secret", time.Now, 20)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	return selector, db
}

func w1xSeedAuthzRow(t *testing.T, db *sql.DB, resourceType, id, resourceID, grantee string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id,
		resource_owner_system_account_id, grantee_system_account_id, status, expires_at,
		effective_source_type, effective_source_team_id, limits_json)
		VALUES (?, ?, ?, 'sys_owner', ?, 'active', NULL, 'direct', NULL, NULL)`, id, resourceType, resourceID, grantee)
	if err != nil {
		t.Fatalf("seed authorization: %v", err)
	}
}

func TestW1XResolveGroupAccessArms(t *testing.T) {
	ctx := context.Background()

	// 高并发分组缺策略 JSON：解析失败。
	fixture, _ := w1xSelectorAuthzFixture(t)
	if _, err := fixture.db.Exec(`UPDATE groups SET group_type = 'high_concurrency', scheduling_policy_json = NULL WHERE id = 'w1x-grp'`); err != nil {
		t.Fatalf("update group: %v", err)
	}
	if _, err := fixture.resolveGroupAccess(ctx, "w1x-grp", "sys_owner"); err == nil {
		t.Fatal("高并发分组缺策略必须报错")
	}

	// 授权组：resource_authorizations 表缺失 -> 授权读取报错。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1nDropTables(t, fixture.db, "resource_authorizations")
	if _, err := fixture.resolveGroupAccess(ctx, "w1x-grp", "sys_other"); err == nil {
		t.Fatal("授权组读取失败必须报错")
	}

	// 授权组：group_authorization_settings 表缺失 -> 本地设置读取报错。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "group", "w1x-authz", "w1x-grp", "sys_other")
	w1nDropTables(t, fixture.db, "group_authorization_settings")
	if _, err := fixture.resolveGroupAccess(ctx, "w1x-grp", "sys_other"); err == nil {
		t.Fatal("本地授权设置读取失败必须报错")
	}

	// 授权组：本地类型为 personal 且无本地策略 -> 回退分组策略（NULL）成功。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "group", "w1x-authz", "w1x-grp", "sys_other")
	if _, err := fixture.db.Exec(`INSERT INTO group_authorization_settings VALUES ('w1x-authz', 'sys_other', 'w1x-grp', 1, 'personal', NULL)`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	meta, err := fixture.resolveGroupAccess(ctx, "w1x-grp", "sys_other")
	if err != nil || meta == nil || meta.GroupAccessType != gatewayruntimecache.GroupAccessTypeAuthorized || meta.GroupType == nil || *meta.GroupType != "personal" {
		t.Fatalf("授权组 personal 回退 = (%+v, %v)", meta, err)
	}

	// 授权组：本地 high_concurrency 策略非法 JSON -> 报错。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "group", "w1x-authz", "w1x-grp", "sys_other")
	if _, err := fixture.db.Exec(`INSERT INTO group_authorization_settings VALUES ('w1x-authz', 'sys_other', 'w1x-grp', 1, 'high_concurrency', 'not-json')`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if _, err := fixture.resolveGroupAccess(ctx, "w1x-grp", "sys_other"); err == nil {
		t.Fatal("本地策略非法 JSON 必须报错")
	}

	// 授权组：本地 high_concurrency 策略合法 -> 授权元数据带配额与来源。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "group", "w1x-authz", "w1x-grp", "sys_other")
	if _, err := fixture.db.Exec(`UPDATE resource_authorizations SET limits_json = '{"hourly":{"enabled":true}}' WHERE id = 'w1x-authz'`); err != nil {
		t.Fatalf("seed limits: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO group_authorization_settings VALUES ('w1x-authz', 'sys_other', 'w1x-grp', 1, 'high_concurrency', '{"clientIpConcurrencyLimit":2}')`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	meta, err = fixture.resolveGroupAccess(ctx, "w1x-grp", "sys_other")
	if err != nil || meta == nil {
		t.Fatalf("授权组 high_concurrency = (%+v, %v)", meta, err)
	}
	if meta.GroupAuthorizationQuotaLimited == nil || !*meta.GroupAuthorizationQuotaLimited {
		t.Fatalf("配额限制投影错误: %+v", meta.GroupAuthorizationQuotaLimited)
	}
	if meta.GroupAuthorizationID == nil || *meta.GroupAuthorizationID != "w1x-authz" {
		t.Fatalf("授权 ID 投影错误: %+v", meta.GroupAuthorizationID)
	}
}

func TestW1XActiveResourceAuthorizationArms(t *testing.T) {
	ctx := context.Background()

	// 表缺失：单条与批量读取都报错。
	fixture, _ := w1xSelectorAuthzFixture(t)
	w1nDropTables(t, fixture.db, "resource_authorizations")
	if _, err := fixture.activeResourceAuthorization(ctx, "account", "res-1", "sys_owner"); err == nil {
		t.Fatal("activeResourceAuthorization 表缺失必须报错")
	}
	if _, err := fixture.activeResourceAuthorizationByID(ctx, "authz-1", "sys_owner"); err == nil {
		t.Fatal("activeResourceAuthorizationByID 表缺失必须报错")
	}
	if _, err := fixture.activeResourceAuthorizationsByIDs(ctx, []string{"authz-1"}, "sys_owner"); err == nil {
		t.Fatal("activeResourceAuthorizationsByIDs 表缺失必须报错")
	}

	// 行存在：单条返回投影，批量按 id 与 resource_id 双键返回。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "account", "authz-1", "res-1", "sys_owner")
	authorization, err := fixture.activeResourceAuthorization(ctx, "account", "res-1", "sys_owner")
	if err != nil || authorization == nil || authorization.id != "authz-1" || authorization.resourceOwnerID != "sys_owner" {
		t.Fatalf("activeResourceAuthorization = (%+v, %v)", authorization, err)
	}
	missing, err := fixture.activeResourceAuthorizationByID(ctx, "authz-none", "sys_owner")
	if missing != nil || err != nil {
		t.Fatalf("activeResourceAuthorizationByID(缺失) = (%+v, %v), want (nil, nil)", missing, err)
	}
	found, err := fixture.activeResourceAuthorizationByID(ctx, "authz-1", "sys_owner")
	if err != nil || found == nil || found.resourceID != "res-1" {
		t.Fatalf("activeResourceAuthorizationByID = (%+v, %v)", found, err)
	}
	batch, err := fixture.activeResourceAuthorizationsByIDs(ctx, []string{"authz-1", "authz-1", "  "}, "sys_owner")
	if err != nil {
		t.Fatalf("activeResourceAuthorizationsByIDs: %v", err)
	}
	if batch["authz-1"] == nil || batch["res-1"] == nil {
		t.Fatalf("批量授权映射必须同时带 id 与 resource_id 键: %v", batch)
	}

	// loadAccountAuthorizationsForSelection：空 authID 集合 -> 空 map。
	rows := []chainCandidateRow{{}, {AccountAuthorizationID: sql.NullString{String: "  ", Valid: true}}}
	loaded, err := fixture.loadAccountAuthorizationsForSelection(ctx, rows,
		&gatewayruntimecache.GroupUsageAccessMetadata{GroupAccessType: gatewayruntimecache.GroupAccessTypeOwner}, "sys_owner")
	if err != nil || len(loaded) != 0 {
		t.Fatalf("loadAccountAuthorizationsForSelection(空集合) = (%v, %v)", loaded, err)
	}
}

func TestW1XResolveChainAccountAccessArms(t *testing.T) {
	ctx := context.Background()
	ownerAccess := &gatewayruntimecache.GroupUsageAccessMetadata{GroupAccessType: gatewayruntimecache.GroupAccessTypeOwner, GroupOwnerSystemAccountID: "sys_owner"}
	authorizedAccess := &gatewayruntimecache.GroupUsageAccessMetadata{GroupAccessType: gatewayruntimecache.GroupAccessTypeAuthorized, GroupOwnerSystemAccountID: "sys_owner"}

	// 授权实例路径：authorizations 预取 map 命中。
	fixture, _ := w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "account", "authz-1", "res-1", "sys_other")
	row := &chainCandidateRow{ID: "acc-1", SystemAccountID: "sys_other",
		AuthorizationInstanceAuthorizationID: sql.NullString{String: "authz-1", Valid: true}}
	prefetched := map[string]*chainAuthorizationRow{"authz-1": {id: "authz-1", resourceOwnerID: "sys_owner"}}
	access, err := fixture.resolveChainAccountAccess(ctx, row, "sys_other", ownerAccess, "authz-1", prefetched)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessAuthorized {
		t.Fatalf("预取 map 命中 = (%+v, %v)", access, err)
	}

	// 授权实例路径：账户属主与调用者不一致 -> nil。
	foreign := &chainCandidateRow{ID: "acc-2", SystemAccountID: "sys_third",
		AuthorizationInstanceAuthorizationID: sql.NullString{String: "authz-1", Valid: true}}
	access, err = fixture.resolveChainAccountAccess(ctx, foreign, "sys_other", ownerAccess, "", nil)
	if access != nil || err != nil {
		t.Fatalf("属主不一致 = (%+v, %v), want (nil, nil)", access, err)
	}

	// 授权实例路径：库读错误 / 无行 / 绑定不匹配 / 绑定匹配。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1nDropTables(t, fixture.db, "resource_authorizations")
	if _, err := fixture.resolveChainAccountAccess(ctx, row, "sys_other", ownerAccess, "", nil); err == nil {
		t.Fatal("ByID 库读错误必须透传")
	}
	fixture, _ = w1xSelectorAuthzFixture(t)
	if access, err := fixture.resolveChainAccountAccess(ctx, row, "sys_other", ownerAccess, "", nil); access != nil || err != nil {
		t.Fatalf("ByID 无行 = (%+v, %v), want (nil, nil)", access, err)
	}
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "account", "authz-1", "res-1", "sys_other")
	if access, err := fixture.resolveChainAccountAccess(ctx, row, "sys_other", ownerAccess, "authz-other", nil); access != nil || err != nil {
		t.Fatalf("绑定不匹配 = (%+v, %v), want (nil, nil)", access, err)
	}
	access, err = fixture.resolveChainAccountAccess(ctx, row, "sys_other", ownerAccess, "authz-1", nil)
	if err != nil || access == nil || access.accountAuthorizationID == nil || *access.accountAuthorizationID != "authz-1" {
		t.Fatalf("绑定匹配 = (%+v, %v)", access, err)
	}

	// 非授权实例路径：owner 直通；授权组回退；owner 组的授权 map 命中/缺失、库读错误、绑定门。
	own := &chainCandidateRow{ID: "acc-3", SystemAccountID: "sys_owner"}
	access, err = fixture.resolveChainAccountAccess(ctx, own, "sys_owner", ownerAccess, "", nil)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessOwner {
		t.Fatalf("owner 直通 = (%+v, %v)", access, err)
	}
	groupOwned := &chainCandidateRow{ID: "acc-4", SystemAccountID: "sys_owner"}
	access, err = fixture.resolveChainAccountAccess(ctx, groupOwned, "sys_other", authorizedAccess, "", nil)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessGroupAuthorized {
		t.Fatalf("授权组回退 = (%+v, %v)", access, err)
	}
	stranger := &chainCandidateRow{ID: "acc-5", SystemAccountID: "sys_third"}
	if access, err := fixture.resolveChainAccountAccess(ctx, stranger, "sys_other", authorizedAccess, "", nil); access != nil || err != nil {
		t.Fatalf("授权组陌生属主 = (%+v, %v), want (nil, nil)", access, err)
	}

	// owner 组：bound 为空时按 row.ID 查预取 map。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "account", "authz-acc5", "acc-5", "sys_other")
	prefetchByID := map[string]*chainAuthorizationRow{"acc-5": {id: "authz-acc5"}}
	access, err = fixture.resolveChainAccountAccess(ctx, stranger, "sys_other", ownerAccess, "", prefetchByID)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessAuthorized {
		t.Fatalf("row.ID 预取命中 = (%+v, %v)", access, err)
	}
	if access, err := fixture.resolveChainAccountAccess(ctx, stranger, "sys_other", ownerAccess, "", map[string]*chainAuthorizationRow{}); access != nil || err != nil {
		t.Fatalf("预取 map 缺失 = (%+v, %v), want (nil, nil)", access, err)
	}

	fixture, _ = w1xSelectorAuthzFixture(t)
	w1nDropTables(t, fixture.db, "resource_authorizations")
	if _, err := fixture.resolveChainAccountAccess(ctx, stranger, "sys_other", ownerAccess, "", nil); err == nil {
		t.Fatal("account 授权库读错误必须透传")
	}

	// account 授权无行：表在但没有匹配授权 -> nil。
	noRow, _ := w1xSelectorAuthzFixture(t)
	if access, err := noRow.resolveChainAccountAccess(ctx, stranger, "sys_other", ownerAccess, "", nil); access != nil || err != nil {
		t.Fatalf("account 授权无行 = (%+v, %v), want (nil, nil)", access, err)
	}

	// account 授权命中 + 绑定门。
	fixture, _ = w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "account", "authz-acc5", "acc-5", "sys_other")
	if access, err := fixture.resolveChainAccountAccess(ctx, stranger, "sys_other", ownerAccess, "authz-other", nil); access != nil || err != nil {
		t.Fatalf("account 绑定门 = (%+v, %v), want (nil, nil)", access, err)
	}
	access, err = fixture.resolveChainAccountAccess(ctx, stranger, "sys_other", ownerAccess, "", nil)
	if err != nil || access == nil || access.accountAccessType != chainAccountAccessAuthorized {
		t.Fatalf("account 授权命中 = (%+v, %v)", access, err)
	}
}

func TestW1XOrderChainCandidateRowsTailArms(t *testing.T) {
	// chainModelRank：nil map / row.ID / row.AccountID / 缺省。
	if got := chainModelRank(&chainCandidateRow{ID: "a"}, nil); got != 0 {
		t.Fatalf("nil modelRanks = %d, want 0", got)
	}
	ranks := map[string]int{"acc-by-id": 1, "acc-by-binding": 2}
	if got := chainModelRank(&chainCandidateRow{ID: "acc-by-id"}, ranks); got != 1 {
		t.Fatalf("row.ID 命中 = %d", got)
	}
	if got := chainModelRank(&chainCandidateRow{ID: "other", AccountID: "acc-by-binding"}, ranks); got != 2 {
		t.Fatalf("row.AccountID 回退 = %d", got)
	}
	if got := chainModelRank(&chainCandidateRow{ID: "miss"}, ranks); got != 3 {
		t.Fatalf("缺省 = %d", got)
	}

	// 排序尾巴：fallback 升序 -> 同名 zh collator -> 同名 id 升序。
	eligible := w1nEligible(
		&chainCandidateRow{ID: "w1x-fb-1", Name: "同名", Priority: 5, LocalFallback: sql.NullInt64{Int64: 1, Valid: true}},
		&chainCandidateRow{ID: "w1x-fb-0", Name: "同名", Priority: 5},
	)
	ordered := orderChainCandidateRowsForDispatch(eligible, nil)
	if ordered[0].row.ID != "w1x-fb-0" {
		t.Fatalf("fallback=0 必须在前: first=%s", ordered[0].row.ID)
	}

	sameName := w1nEligible(
		&chainCandidateRow{ID: "w1x-z", Name: "同名", Priority: 5},
		&chainCandidateRow{ID: "w1x-a", Name: "同名", Priority: 5},
	)
	ordered = orderChainCandidateRowsForDispatch(sameName, nil)
	if ordered[0].row.ID != "w1x-a" {
		t.Fatalf("同名必须按 id 升序: first=%s", ordered[0].row.ID)
	}
}

func TestW1XListAccountsForGroupEligibilityArms(t *testing.T) {
	ctx := context.Background()
	authorizedAccess := &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: "sys_admin",
		ProviderCode:              "openai",
		GroupAccessType:           gatewayruntimecache.GroupAccessTypeAuthorized,
	}
	ownerAccess := &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: "sys_admin",
		ProviderCode:              "openai",
		GroupAccessType:           gatewayruntimecache.GroupAccessTypeOwner,
	}

	t.Run("groups 表缺失 -> 分组解析报错", func(t *testing.T) {
		db := seedChainSelectorDB(t)
		t.Cleanup(func() { _ = db.Close() })
		w1nDropTables(t, db, "groups")
		selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 20)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}
		if _, err := selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin", gatewayruntimecache.OpenAIAccountsForGroupOptions{}); err == nil {
			t.Fatal("分组解析失败必须报错")
		}
	})

	t.Run("授权组预解析 + 授权表缺失 -> access 解析报错", func(t *testing.T) {
		db := seedChainSelectorDB(t)
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`UPDATE accounts SET authorization_instance_authorization_id = 'w1x-authz',
			authorization_instance_source_account_id = 'w1x-src-1' WHERE id = 'a-limit-acc'`); err != nil {
			t.Fatalf("bind authz instance: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable,
			concurrency_limit, priority, super_priority_enabled, fallback_enabled, config_revision, dispatch_revision, deleted_at)
			VALUES ('w1x-src-1', 'sys_admin', 'openai', 'w1x-src-1', 'api_key', 'active', 1, 0, 0, 0, 0, 1, 1, NULL)`); err != nil {
			t.Fatalf("seed source account: %v", err)
		}
		selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 20)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}
		_, err = selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: authorizedAccess})
		if err == nil {
			t.Fatal("授权表缺失时 access 解析必须报错")
		}
	})

	t.Run("owner 组 + 授权表缺失 -> 授权预取报错", func(t *testing.T) {
		db := seedChainSelectorDB(t)
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`UPDATE accounts SET authorization_instance_authorization_id = 'w1x-authz',
			authorization_instance_source_account_id = 'w1x-src-1' WHERE id = 'a-limit-acc'`); err != nil {
			t.Fatalf("bind authz instance: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable,
			concurrency_limit, priority, super_priority_enabled, fallback_enabled, config_revision, dispatch_revision, deleted_at)
			VALUES ('w1x-src-1', 'sys_admin', 'openai', 'w1x-src-1', 'api_key', 'active', 1, 0, 0, 0, 0, 1, 1, NULL)`); err != nil {
			t.Fatalf("seed source account: %v", err)
		}
		selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 20)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}
		if _, err := selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: ownerAccess}); err == nil {
			t.Fatal("owner 组授权预取失败必须报错")
		}
	})

	t.Run("账户过期时间非法 -> 可用性门报错", func(t *testing.T) {
		db := seedChainSelectorDB(t)
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`UPDATE accounts SET account_expires_at = 'bad-date' WHERE id = 'a-limit-acc'`); err != nil {
			t.Fatalf("bind expires: %v", err)
		}
		selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 20)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}
		if _, err := selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: ownerAccess}); err == nil {
			t.Fatal("非法过期时间必须报错")
		}
	})

	t.Run("授权绑定不匹配 -> 候选被丢弃 / 匹配 -> 水合成功", func(t *testing.T) {
		build := func(t *testing.T, bindingID string) (*sql.DB, *chainAccountsSelector) {
			db := seedChainSelectorDB(t)
			t.Cleanup(func() { _ = db.Close() })
			sealed, sealErr := accounts.EncryptJSON("secret", map[string]any{"api_key": "sk-w1x-binding"})
			if sealErr != nil {
				t.Fatalf("encrypt credentials: %v", sealErr)
			}
			for _, statement := range []string{
				`CREATE TABLE resource_authorizations (
					id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT,
					resource_owner_system_account_id TEXT, grantee_system_account_id TEXT,
					status TEXT, expires_at TEXT, effective_source_type TEXT, effective_source_team_id TEXT, limits_json TEXT)`,
				`INSERT INTO resource_authorizations VALUES ('w1x-authz-1', 'account', 'w1x-src-1', 'sys_admin', 'sys_admin', 'active', NULL, NULL, NULL, NULL)`,
				`UPDATE accounts SET authorization_instance_authorization_id = 'w1x-authz-1',
					authorization_instance_source_account_id = 'w1x-src-1' WHERE id = 'a-limit-acc'`,
				`INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, schedulable,
					concurrency_limit, priority, super_priority_enabled, fallback_enabled, config_revision, dispatch_revision, credentials_encrypted, deleted_at)
					VALUES ('w1x-src-1', 'sys_admin', 'openai', 'w1x-src-1', 'api_key', 'active', 1, 0, 0, 0, 0, 1, 1, '` + sealed + `', NULL)`,
				`DELETE FROM group_accounts WHERE account_id = 'a-limit-acc'`,
				`UPDATE group_accounts SET enabled = 0 WHERE account_id <> 'a-limit-acc'`,
				`INSERT INTO group_accounts VALUES ('a-limit-acc', 'sys_admin', 'grp-limit', '` + bindingID + `', 0, 0, 0, 1, '2026-01-01T00:00:00.000Z')`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatalf("seed %q: %v", statement[:32], err)
				}
			}
			selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 20)
			if err != nil {
				t.Fatalf("create selector: %v", err)
			}
			return db, selector
		}

		_, mismatched := build(t, "w1x-authz-other")
		result, err := mismatched.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: ownerAccess})
		if err != nil {
			t.Fatalf("绑定不匹配: %v", err)
		}
		if len(result.Accounts) != 0 || result.Diagnostics == nil || result.Diagnostics.EligibleRowCount != 0 {
			t.Fatalf("绑定不匹配必须丢弃候选: %+v", result.Diagnostics)
		}

		_, matched := build(t, "w1x-authz-1")
		result, err = matched.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: ownerAccess})
		if err != nil {
			t.Fatalf("绑定匹配: %v", err)
		}
		if len(result.Accounts) != 1 || result.Accounts[0].ID != "a-limit-acc" {
			t.Fatalf("绑定匹配必须水合账户: %+v", result.Accounts)
		}
		if result.Accounts[0].AccountAccessType != string(chainAccountAccessAuthorized) {
			t.Fatalf("access 类型 = %q, want account_authorized", result.Accounts[0].AccountAccessType)
		}
	})

	t.Run("模型窗口 includeUnavailable + 不可用账户纳入", func(t *testing.T) {
		db := seedChainSelectorDB(t)
		t.Cleanup(func() { _ = db.Close() })
		// 模型窗口 CTE 引用 provider_code，本地 schema 需补列。
		for _, alter := range []string{
			`ALTER TABLE account_supported_models ADD COLUMN provider_code TEXT`,
			`ALTER TABLE account_model_mappings ADD COLUMN provider_code TEXT`,
		} {
			if _, err := db.Exec(alter); err != nil {
				t.Fatalf("alter model schema: %v", err)
			}
		}
		if _, err := db.Exec(`UPDATE accounts SET status = 'rate_limited' WHERE id = 'a-limit-acc'`); err != nil {
			t.Fatalf("degrade account: %v", err)
		}
		// 水合阶段要求可解密凭据，否则账户被静默丢弃。
		sealed, sealErr := accounts.EncryptJSON("secret", map[string]any{"api_key": "sk-w1x-model"})
		if sealErr != nil {
			t.Fatalf("encrypt credentials: %v", sealErr)
		}
		if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = 'a-limit-acc'`, sealed); err != nil {
			t.Fatalf("bind credentials: %v", err)
		}
		selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 20)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}
		result, err := selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{
				PreResolvedGroupAccess:  ownerAccess,
				RequestedModel:          "gpt-w1x",
				RequestedEndpointFamily: "chat",
				IncludeUnavailable:      true,
			})
		if err != nil {
			t.Fatalf("模型窗口宽限读取: %v", err)
		}
		found := false
		for _, account := range result.Accounts {
			if account.ID == "a-limit-acc" {
				found = true
			}
		}
		if !found {
			t.Fatalf("includeUnavailable 必须保留 rate_limited 账户: %+v", result.Accounts)
		}
	})
}

// ---------------------------------------------------------------------------
// 3. chain_dispatch.go
// ---------------------------------------------------------------------------

func TestW1XDispatchAdapterPassthroughArms(t *testing.T) {
	ctx := context.Background()

	// degradedProxyHealth 的失败记录保持 no-op。
	if err := (&degradedProxyHealth{}).RecordFailureAsync(ctx, gatewaydispatch.AccountCandidate{ID: "a"}, "boom"); err != nil {
		t.Fatalf("degraded RecordFailureAsync = %v, want nil", err)
	}

	// 时延降级端口的空参直通臂。
	port := chainLatencyDegradationPort{}
	degraded, err := port.IsAccountLatencyDegradedAsync(ctx, gatewaydispatch.AccountCandidate{ID: "a"}, nil)
	if degraded || err != nil {
		t.Fatalf("IsAccountLatencyDegradedAsync(nil scope) = (%v, %v)", degraded, err)
	}
	if result, err := port.RecordFirstByteSlowAsync(ctx, gatewaydispatch.AccountCandidate{ID: "a"}, nil, nil, "slow"); result != nil || err != nil {
		t.Fatalf("RecordFirstByteSlowAsync(nil scope) = (%v, %v)", result, err)
	}
	if result, err := port.RecordFirstByteSuccessAsync(ctx, gatewaydispatch.AccountCandidate{ID: "a"}, nil, nil, 12); result != nil || err != nil {
		t.Fatalf("RecordFirstByteSuccessAsync(nil scope) = (%v, %v)", result, err)
	}
	// 服务在场但 speedFirst 配置无 deadline：决策面解码为 nil，排序直通。
	livePort := chainLatencyDegradationPort{service: gatewayproxyhealth.NewLatencyDegradationService(
		gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), nil, gatewayproxyhealth.LatencyDegradationOptions{})}
	order, err := livePort.OrderAsync(ctx, []gatewaydispatch.AccountCandidate{{ID: "a"}},
		&gatewaydispatch.LatencyScopeInput{SystemAccountID: "sys"}, &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{}, nil)
	if err != nil || len(order.Accounts) != 1 || order.Applied {
		t.Fatalf("OrderAsync(无 deadline 配置) = (%+v, %v), want 直通", order, err)
	}

	// chainSpeedFirstRuntimeConfigOf：Raw 非对象时回落默认旋钮。
	typed := chainSpeedFirstRuntimeConfigOf(&gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		FirstByteDeadlineMs: w1xInt64Ptr(1_500),
		Raw:                 map[string]any{"speedFirstConfig": "not-an-object"},
	})
	if typed == nil || typed.FirstByteDeadlineMs != 1_500 || typed.SlowTriggerCount != 3 || typed.MaxFirstByteRetriesPerRequest != 2 {
		t.Fatalf("默认旋钮 = %+v", typed)
	}

	// envBoolOf 全分支。
	if !envBoolOf("1") || !envBoolOf(" true ") || !envBoolOf("YES") || !envBoolOf("on") {
		t.Fatal("真值字面量必须识别")
	}
	if envBoolOf("0") || envBoolOf("") || envBoolOf("false") {
		t.Fatal("假值字面量必须拒绝")
	}
}

func TestW1XClientSourceAvoidanceErrorArm(t *testing.T) {
	adapter := &chainClientSourceAvoidance{turnRetry: &gatewaycodex.TurnRetryService{
		Secret: "w1x-secret",
		Store:  w1xFailingTurnStore{},
	}}
	strategy := gatewaypreauth.ClientStrategyContext{
		Opaque: gatewaycodex.OpenAIGatewayClientStrategyContext{ClientSourceAvoidanceStateKey: "w1x-key"},
	}
	_, err := adapter.OrderAsync(context.Background(),
		[]gatewaydispatch.AccountCandidate{{ID: "a"}}, strategy, nil)
	if err == nil || !strings.Contains(err.Error(), "w1x 状态存储读取失败") {
		t.Fatalf("状态存储错误必须向上透传: %v", err)
	}
}

func TestW1XDispatchQuotaCheckBatchErrorArm(t *testing.T) {
	statsDB := w1xOpenBareSQLite(t, "w1x-quota-stats.sqlite3")
	statsStore, err := gatewayquota.NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatalf("create stats store: %v", err)
	}
	snapshot, err := gatewayquota.NewSnapshotCache(gatewayquota.Modes{}, nil, time.Now, nil)
	if err != nil {
		t.Fatalf("create snapshot cache: %v", err)
	}
	timezone := settingsTimezoneProvider{read: func(string) (string, error) { return "UTC", nil }}
	authzQuota, err := gatewayquota.NewAuthorizationQuotaService(gatewayquota.AuthorizationQuotaConfig{
		Modes:    gatewayquota.Modes{},
		Business: statsDB,
		Stats:    statsStore,
		Timezone: timezone,
		Snapshot: snapshot,
		Now:      time.Now,
	})
	if err != nil {
		t.Fatalf("create authz quota service: %v", err)
	}
	quota := newChainDispatchQuota(authzQuota)
	_, err = quota.CheckBatchAsync(context.Background(), gatewayruntimecache.GroupUsageAccessMetadata{
		GroupAuthorizationID:           strPtr("w1x-group-authz"),
		GroupAuthorizationQuotaLimited: boolPtr(false),
	}, []gatewaydispatch.AccountCandidate{{ID: "w1x-acc"}})
	if err == nil {
		t.Fatal("stats 表缺失时批量配额读取必须报错")
	}
}

func TestW1XRuntimeCacheAndGuardArms(t *testing.T) {
	ctx := context.Background()

	// 空库缓存：分组用量元数据解析必须报错。
	bare := w1xOpenBareSQLite(t, "w1x-cache-bare.sqlite3")
	models, err := gatewayruntimecache.NewSQLReadModels(bare, false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatalf("create read models: %v", err)
	}
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	t.Cleanup(cache.Close)
	port := newChainRuntimeCachePort(cache)
	if _, _, err := port.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "grp", "sys"); err == nil {
		t.Fatal("空库上分组元数据解析必须报错")
	}

	// 响应检查副作用：设置读取失败必须报错（空库无 system_settings）。
	effects := &chainResponseAccountEffects{cache: cache}
	decision := &gatewayresponse.ResponseInspectionDecision{
		Reason:       "configured_response_policy",
		Action:       "replace_with_failure",
		AccountState: "runtime_avoidance",
	}
	view := gatewayresponse.OpenAIAccountView{
		Account: gatewayruntimecache.OpenAIAccountSecret{ID: "w1x-acc", AccountAccessType: "owner"},
	}
	if err := effects.ApplyInspectionPolicySideEffects(decision, view, true); err == nil {
		t.Fatal("设置读取失败必须报错")
	}
}

// w1xGuardRedisFixture 建一个 redis 驱动的 API Key 失败守卫（miniredis 后端）。
func w1xGuardRedisFixture(t *testing.T, server *miniredis.Miniredis) *gatewayaccounteffects.AccountAPIKeyFailureGuard {
	t.Helper()
	factory := func() (gatewayaccounteffects.AccountApiKeyTransientStateStore, error) {
		return gatewayaccounteffects.NewRedisAccountApiKeyTransientStateStore(
			gatewayaccounteffects.RedisAccountApiKeyTransientStateStoreOptions{
				RedisURL:  "redis://" + server.Addr(),
				Namespace: "w1x-test",
			})
	}
	return gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
		gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "redis"},
		gatewayaccounteffects.SystemClock{}, nil, factory)
}

func TestW1XLoadTransientStatesWithGeneration(t *testing.T) {
	server := miniredis.RunT(t)
	guard := w1xGuardRedisFixture(t, server)
	generation := "gen-w1x-1"
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                                "w1x-acc",
		SelectedAPIKeyFingerprint:         strPtr("fp-w1x"),
		SelectedAPIKeyTransientGeneration: &generation,
	}
	if _, err := guard.RecordTransientFailure(context.Background(), account, gatewayaccounteffects.AccountApiKeyFailureStatus("rate_limited")); err != nil {
		t.Fatalf("record transient failure: %v", err)
	}
	port := newChainRuntimeCachePort(nil)
	port.guard = guard
	states, err := port.LoadApiKeyTransientStatesForDispatch(context.Background(), "w1x-acc", []string{"fp-w1x"})
	if err != nil {
		t.Fatalf("LoadApiKeyTransientStatesForDispatch: %v", err)
	}
	if len(states) != 1 || states[0].Generation == nil || strings.TrimSpace(*states[0].Generation) == "" {
		t.Fatalf("瞬态代数投影错误: %+v", states)
	}
}

func TestW1XCaptureFailureObservationArms(t *testing.T) {
	guard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
		gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "memory"},
		gatewayaccounteffects.SystemClock{}, nil, nil)
	port := chainAPIKeyEffectsPort{guard: guard}

	// 无选中指纹：无法定位 Key 运行态 -> 空观察。
	if observation := port.CaptureFailureObservation(gatewaydispatch.AccountCandidate{ID: "w1x-acc"}); observation != "" {
		t.Fatalf("无指纹观察 = %q, want 空串", observation)
	}
	// 带指纹：观察 epoch 为十进制整数文本。
	observation := port.CaptureFailureObservation(gatewaydispatch.AccountCandidate{
		ID:                        "w1x-acc",
		SelectedAPIKeyFingerprint: strPtr("fp-w1x"),
	})
	if observation == "" {
		t.Fatal("带指纹必须产生观察 epoch")
	}
	for _, symbol := range observation {
		if symbol < '0' || symbol > '9' {
			t.Fatalf("观察 epoch 必须是十进制文本: %q", observation)
		}
	}
}

func TestW1XConfiguredPolicyResolveLocalArms(t *testing.T) {
	ctx := context.Background()
	long := int64(24 * 3600)

	build := func(t *testing.T) (*chainConfiguredPolicyAvoidanceSuppression, *gatewayaccounteffects.ConfiguredPolicyAvoidanceService) {
		t.Helper()
		service := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, nil)
		inner := chainSuppressionPort{
			store:  gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{}),
			waiter: gatewaycircuit.NewPreAuthRecoverableWait(nil, nil),
		}
		return &chainConfiguredPolicyAvoidanceSuppression{inner: inner, avoidance: service}, service
	}

	t.Run("全量避让 -> 503 完成契约", func(t *testing.T) {
		suppression, service := build(t)
		for _, accountID := range []string{"w1x-a", "w1x-b"} {
			if err := service.SuppressGatewayAccountLocallyForSeconds(ctx,
				gatewayaccounteffects.SuppressibleGatewayAccount{ID: accountID, AccountAccessType: "owner"}, &long, "w1x 全量避让"); err != nil {
				t.Fatalf("写入避让: %v", err)
			}
		}
		coordinator := &w1xRouteCoordinator{}
		capture := &w1rAuditCapture{}
		startedAt := time.Now().Add(-5 * time.Second).UnixMilli()
		resolved, completed, err := suppression.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
			Accounts:          []gatewaydispatch.AccountCandidate{{ID: "w1x-a", AccountAccessType: "owner"}, {ID: "w1x-b", AccountAccessType: "owner"}},
			AuditCapture:      gatewaydispatch.AuditCapture{Context: capture},
			StartedAt:         startedAt,
			ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
			RouteCoordinator:  coordinator,
		})
		if err != nil || !completed || resolved != nil {
			t.Fatalf("全量避让 = (%+v, %v, %v), want completed", resolved, completed, err)
		}
		if len(coordinator.failures) != 1 || coordinator.failures[0].FailureAttribution != "gateway_capacity" {
			t.Fatalf("完成契约错误: %+v", coordinator.failures)
		}
		if coordinator.failures[0].StatusCode != 503 {
			t.Fatalf("全量避让必须回 503: %+v", coordinator.failures[0])
		}
		found := false
		for _, entry := range capture.entries {
			if entry.label == "local_account_suppression" {
				found = true
			}
		}
		if !found {
			t.Fatalf("必须记录 local_account_suppression 审计: %+v", capture.entries)
		}
	})

	t.Run("全量避让 + 完成失败 -> 错误透传", func(t *testing.T) {
		suppression, service := build(t)
		if err := service.SuppressGatewayAccountLocallyForSeconds(ctx,
			gatewayaccounteffects.SuppressibleGatewayAccount{ID: "w1x-a", AccountAccessType: "owner"}, &long, "w1x 避让"); err != nil {
			t.Fatalf("写入避让: %v", err)
		}
		coordinator := &w1xRouteCoordinator{err: errors.New("w1x 完成失败")}
		_, _, err := suppression.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
			Accounts:          []gatewaydispatch.AccountCandidate{{ID: "w1x-a", AccountAccessType: "owner"}},
			StartedAt:         time.Now().Add(-5 * time.Second).UnixMilli(),
			ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
			RouteCoordinator:  coordinator,
		})
		if err == nil || !strings.Contains(err.Error(), "w1x 完成失败") {
			t.Fatalf("CompleteFailure 错误必须透传: %v", err)
		}
	})

	t.Run("部分避让 -> 合并幸存面", func(t *testing.T) {
		suppression, service := build(t)
		if err := service.SuppressGatewayAccountLocallyForSeconds(ctx,
			gatewayaccounteffects.SuppressibleGatewayAccount{ID: "w1x-a", AccountAccessType: "owner"}, &long, "w1x 部分避让"); err != nil {
			t.Fatalf("写入避让: %v", err)
		}
		coordinator := &w1xRouteCoordinator{}
		resolved, completed, err := suppression.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
			Accounts:          []gatewaydispatch.AccountCandidate{{ID: "w1x-a", AccountAccessType: "owner"}, {ID: "w1x-b", AccountAccessType: "owner"}},
			StartedAt:         time.Now().UnixMilli(),
			ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
			RouteCoordinator:  coordinator,
		})
		if err != nil || completed || resolved == nil {
			t.Fatalf("部分避让 = (%+v, %v, %v)", resolved, completed, err)
		}
		if len(resolved.Accounts) != 1 || resolved.Accounts[0].ID != "w1x-b" || resolved.SuppressedCount != 1 {
			t.Fatalf("合并结果错误: %+v", resolved)
		}
		if len(coordinator.failures) != 0 {
			t.Fatalf("部分避让不得完成请求: %+v", coordinator.failures)
		}
	})
}

func TestW1XListAvailabilityDirtyMarkerArms(t *testing.T) {
	ctx := context.Background()

	// 非 postgres 方言：写面缺席。2026-09-21 起投影开关移除，postgres
	// 方言恒装配脏标记。
	db := w1xOpenBareSQLite(t, "w1x-marker.sqlite3")
	composed := &composition{db: db}
	if marker := newChainListAvailabilityDirtyMarker(composed); marker != nil {
		t.Fatal("sqlite 方言不得装配脏标记")
	}
	pgComposed := &composition{db: db, pgDialect: true}
	marker := newChainListAvailabilityDirtyMarker(pgComposed)
	if marker == nil {
		t.Fatal("postgres 方言必须装配脏标记")
	}

	// 空白 / 超长来源账户：直接 no-op。
	if err := marker(ctx, "   ", "reason", 1); err != nil {
		t.Fatalf("空白来源必须 no-op: %v", err)
	}
	if err := marker(ctx, strings.Repeat("x", 257), "reason", 1); err != nil {
		t.Fatalf("超长来源必须 no-op: %v", err)
	}

	// 已关闭句柄：事务开启失败透传。
	closedDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1x-closed.sqlite3"))
	if err != nil {
		t.Fatalf("open closed-case db: %v", err)
	}
	closedMarker := newChainListAvailabilityDirtyMarker(&composition{db: closedDB, pgDialect: true})
	_ = closedDB.Close()
	if err := closedMarker(ctx, "w1x-src", "reason", 1); err == nil {
		t.Fatal("关闭句柄必须透传事务错误")
	}

	// 完整链路：sqlite ATTACH juhe_business 模拟 PG 限定名，家族展开 + upsert。
	attach := w1xOpenBareSQLite(t, "w1x-marker-full.sqlite3")
	if _, err := attach.Exec(`ATTACH DATABASE '` + filepath.Join(t.TempDir(), "w1x-attached.sqlite3") + `' AS juhe_business`); err != nil {
		t.Fatalf("attach juhe_business: %v", err)
	}
	attach.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE juhe_business.accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
			authorization_instance_source_account_id TEXT, deleted_at TEXT)`,
		`CREATE TABLE juhe_business.account_list_availability_projections (
			account_id TEXT PRIMARY KEY, source_generation INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE juhe_business.account_list_availability_dirty (
			account_id TEXT PRIMARY KEY, viewer_system_account_id TEXT, generation INTEGER NOT NULL DEFAULT 0,
			applied_generation INTEGER NOT NULL DEFAULT 0, reason TEXT, available_at_ms INTEGER,
			claim_token TEXT, claimed_by TEXT, claim_until_ms INTEGER, attempt_count INTEGER NOT NULL DEFAULT 0,
			created_at_ms INTEGER, updated_at_ms INTEGER)`,
		`INSERT INTO juhe_business.accounts (id, system_account_id, deleted_at) VALUES ('w1x-src', 'sys_owner', NULL)`,
		`INSERT INTO juhe_business.accounts (id, system_account_id, authorization_instance_source_account_id, deleted_at)
			VALUES ('w1x-child', 'sys_owner', 'w1x-src', NULL)`,
		`INSERT INTO juhe_business.account_list_availability_projections VALUES ('w1x-src', 4)`,
	} {
		if _, err := attach.Exec(statement); err != nil {
			t.Fatalf("seed attached schema: %v", err)
		}
	}
	fullComposed := &composition{db: attach, pgDialect: true}
	fullMarker := newChainListAvailabilityDirtyMarker(fullComposed)
	if err := fullMarker(ctx, "w1x-src", "w1x 失败", 1234); err != nil {
		t.Fatalf("脏标记写入: %v", err)
	}
	var generation int64
	var reason string
	if err := attach.QueryRow(`SELECT generation, reason FROM juhe_business.account_list_availability_dirty WHERE account_id = 'w1x-src'`).
		Scan(&generation, &reason); err != nil {
		t.Fatalf("read dirty row: %v", err)
	}
	if generation != 5 || reason != "w1x 失败" {
		t.Fatalf("脏行 = (gen %d, reason %q), want (5, w1x 失败)", generation, reason)
	}
	var childRows int
	if err := attach.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_list_availability_dirty WHERE account_id = 'w1x-child'`).
		Scan(&childRows); err != nil {
		t.Fatalf("read child row: %v", err)
	}
	if childRows != 1 {
		t.Fatalf("授权实例子账户必须一并入脏: %d", childRows)
	}
	if err := fullMarker(ctx, "w1x-src", "w1x 失败-2", 2345); err != nil {
		t.Fatalf("重复写入: %v", err)
	}
	if err := attach.QueryRow(`SELECT generation FROM juhe_business.account_list_availability_dirty WHERE account_id = 'w1x-src'`).
		Scan(&generation); err != nil {
		t.Fatalf("reread dirty row: %v", err)
	}
	if generation != 6 {
		t.Fatalf("重复写入必须自增 generation: %d", generation)
	}
}

// ---------------------------------------------------------------------------
// 4. chain_wiring_w2c.go
// ---------------------------------------------------------------------------

func TestW1XCacheBindingRowOfAndConcurrencyError(t *testing.T) {
	// 权重越界回落 1，合法权重保留。
	if row := chainCacheBindingRowOf(gatewayrouting.GroupBindingRow{ID: "b1", Weight: nil}); row.Weight != 1 {
		t.Fatalf("nil 权重 = %d, want 1", row.Weight)
	}
	if row := chainCacheBindingRowOf(gatewayrouting.GroupBindingRow{ID: "b1", Weight: w1xInt64Ptr(0)}); row.Weight != 1 {
		t.Fatalf("0 权重 = %d, want 1", row.Weight)
	}
	if row := chainCacheBindingRowOf(gatewayrouting.GroupBindingRow{ID: "b1", Weight: w1xInt64Ptr(101)}); row.Weight != 1 {
		t.Fatalf("101 权重 = %d, want 1", row.Weight)
	}
	row := chainCacheBindingRowOf(gatewayrouting.GroupBindingRow{ID: "b1", APIKeyID: "key", SystemAccountID: "sys",
		GroupID: "grp", Priority: 3, Weight: w1xInt64Ptr(50), Status: "active", ProviderCode: "openai", GroupEnabled: 1})
	if row.Weight != 50 || row.Priority != 3 || row.GroupEnabled != 1 || row.ProviderCode != "openai" {
		t.Fatalf("缓存行投影错误: %+v", row)
	}

	// 并发槽：非法调度策略必须报错。
	slots, err := gatewayclientip.NewClientIPConcurrency(gatewayclientip.ClientIPConcurrencyOptions{})
	if err != nil {
		t.Fatalf("create client ip concurrency: %v", err)
	}
	t.Cleanup(slots.Close)
	adapter := newChainClientIPConcurrency(slots)
	policy := gatewayruntimecache.GroupSchedulingPolicy{"maxQueueSize": 0}
	if _, err := adapter.Acquire(context.Background(), gatewaydispatch.ClientIPConcurrencyInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "1.2.3.4", Policy: &policy,
	}); err == nil {
		t.Fatal("非法 maxQueueSize 必须报错")
	}
}

func TestW1XAccountCircuitServiceForkArms(t *testing.T) {
	// redis 驱动缺 URL：fail-fast。
	if _, _, err := newChainAccountCircuitService("redis", "", "w1x"); err == nil {
		t.Fatal("redis 驱动缺状态 URL 必须报错")
	}

	// redis 驱动 + miniredis：服务可用且关闭句柄安全。
	server := miniredis.RunT(t)
	service, closeRedis, err := newChainAccountCircuitService("redis", "redis://"+server.Addr(), "w1x")
	if err != nil {
		t.Fatalf("create redis circuit service: %v", err)
	}
	if service == nil {
		t.Fatal("redis 驱动必须返回电路服务")
	}
	closeRedis()

	// memory 驱动：进程内存储。
	service, closeMemory, err := newChainAccountCircuitService("memory", "", "")
	if err != nil {
		t.Fatalf("create memory circuit service: %v", err)
	}
	if service == nil {
		t.Fatal("memory 驱动必须返回电路服务")
	}
	closeMemory()
}

func TestW1XKeyModelAdmissionPrepareBridge(t *testing.T) {
	stores := newChainKeyModelRuntimeStoreSelector("", "w1x")
	store, err := stores.Select("memory")
	if err != nil {
		t.Fatalf("select memory key-model store: %v", err)
	}
	request := newW2CTestRequest(t, `{"model":"gpt-4o","stream":true}`)
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "w1x-acc",
		DispatchRevision:          int64PtrOf(1),
		SelectedAPIKeyFingerprint: strPtr("fp-w1x"),
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
	}
	route := gatewaydispatch.ResolveGatewayKeyModelAttemptCapability(request, account)
	if route == nil {
		t.Fatal("完整路由必须解析出 capability")
	}
	preparation, err := (chainKeyModelAdmission{}).Prepare(context.Background(), store,
		gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput{Route: *route, RequestID: "req-w1x", AttemptID: "att-w1x"})
	if err != nil {
		t.Fatalf("Prepare 桥接: %v", err)
	}
	if preparation.CapabilityHash == "" {
		t.Fatalf("Prepare 必须返回能力哈希: %+v", preparation)
	}
}

func TestW1XSuppressionAdapterArms(t *testing.T) {
	store := gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{})

	// lease 适配器的 Generation / CompleteSuccess 固定契约。
	lease := &chainSuppressionLease{lease: gatewaycircuit.HalfOpenLease{
		RuntimeKey: "w1x-acc", AccountID: "w1x-acc", LeaseID: "lease-w1x",
		Release: func() bool { return store.ReleaseHalfOpenLease("w1x-acc", "w1x-acc", "lease-w1x") },
	}}
	if lease.Generation() != nil {
		t.Fatal("本地屏蔽租约无代数，Generation 必须为 nil")
	}
	if changed, err := lease.CompleteSuccess(); changed || err != nil {
		t.Fatalf("CompleteSuccess = (%v, %v), want (false, nil)", changed, err)
	}

	// chainSuppressionResultOf：租约与预检范围的投影。
	released := false
	source := gatewaycircuit.SuppressionFilterResult{
		Accounts:                             []gatewaycircuit.SuppressibleAccount{{SuppressibleGatewayAccount: gatewaycircuit.SuppressibleGatewayAccount{ID: "w1x-keep"}}},
		SuppressedCount:                      2,
		SuppressedAccountIDs:                 []string{"w1x-gone"},
		AcquiredHalfOpenLeases:               []gatewaycircuit.HalfOpenLease{{RuntimeKey: "w1x-keep", AccountID: "w1x-keep", LeaseID: "lease-w1x", Release: func() bool { released = true; return true }}},
		PrecheckSuppressedAccountIDs:         []string{"w1x-pre"},
		ConfiguredPolicySuppressedAccountIDs: []string{"w1x-cfg"},
		PrecheckSuppressedRuntimeScopes:      []gatewaycircuit.PrecheckSuppressedRuntimeScope{{RuntimeKey: "w1x-pre", Generation: 7}},
		NextRetryAfterMs:                     w1xInt64Ptr(1500),
	}
	projected := chainSuppressionResultOf(source, []gatewaydispatch.AccountCandidate{{ID: "w1x-keep"}, {ID: "w1x-gone"}})
	if len(projected.Accounts) != 1 || projected.Accounts[0].ID != "w1x-keep" {
		t.Fatalf("幸存账户投影错误: %+v", projected.Accounts)
	}
	if projected.SuppressedCount != 2 || len(projected.SuppressedAccountIDs) != 1 || projected.SuppressedAccountIDs[0] != "w1x-gone" {
		t.Fatalf("屏蔽元数据投影错误: %+v", projected)
	}
	if projected.NextRetryAfterMs == nil || *projected.NextRetryAfterMs != 1500 {
		t.Fatalf("重试时间投影错误: %+v", projected.NextRetryAfterMs)
	}
	if len(projected.PrecheckSuppressedAccountIDs) != 1 || projected.PrecheckSuppressedAccountIDs[0] != "w1x-pre" {
		t.Fatalf("预检名单投影错误: %+v", projected)
	}
	if len(projected.ConfiguredPolicySuppressedAccountIDs) != 1 || projected.ConfiguredPolicySuppressedAccountIDs[0] != "w1x-cfg" {
		t.Fatalf("配置策略名单投影错误: %+v", projected)
	}
	if len(projected.PrecheckSuppressedRuntimeScopes) != 1 || projected.PrecheckSuppressedRuntimeScopes[0].RuntimeKey != "w1x-pre" || projected.PrecheckSuppressedRuntimeScopes[0].Generation != 7 {
		t.Fatalf("预检范围投影错误: %+v", projected.PrecheckSuppressedRuntimeScopes)
	}
	if len(projected.AcquiredHalfOpenLeases) != 1 {
		t.Fatalf("租约投影缺失: %+v", projected.AcquiredHalfOpenLeases)
	}
	bridged := projected.AcquiredHalfOpenLeases[0]
	if bridged.RuntimeKey() != "w1x-keep" {
		t.Fatalf("租约运行键 = %q", bridged.RuntimeKey())
	}
	if ok, err := bridged.Release(); err != nil || !ok || !released {
		t.Fatalf("租约释放桥接错误: (%v, %v, released=%v)", ok, err, released)
	}

	// derefInt64Ptr / modelPriorityRankMapOf / chainLatencyDegradedOf 小助手。
	if got := derefInt64Ptr(nil); got != 0 {
		t.Fatalf("derefInt64Ptr(nil) = %d", got)
	}
	if got := derefInt64Ptr(w1xInt64Ptr(9)); got != 9 {
		t.Fatalf("derefInt64Ptr(9) = %d", got)
	}
	if ranks := modelPriorityRankMapOf(&gatewaydispatch.ModelPriority{RankByAccountID: map[string]int{"a": 1}}); ranks["a"] != 1 {
		t.Fatalf("modelPriorityRankMapOf 非nil 透传错误: %v", ranks)
	}
	if chainLatencyDegradedOf(nil) != nil || chainLatencyDegradedOf(map[string]struct{}{}) != nil {
		t.Fatal("空降级集合必须返回 nil")
	}
	degraded := chainLatencyDegradedOf(map[string]struct{}{"a": {}, "b": {}})
	if !degraded["a"] || !degraded["b"] || len(degraded) != 2 {
		t.Fatalf("降级集合投影错误: %v", degraded)
	}

	// 结构化日志漏斗：仅验证可调用且不 panic。
	chainSuppressionLogger{}.Warn(map[string]any{"k": "v"}, "w1x 屏蔽警告")
	chainSuppressionLogger{}.Info(map[string]any{"k": "v"}, "w1x 半开提示")
}

func TestW1XSuppressionResolveExhausted503Arms(t *testing.T) {
	ctx := context.Background()

	build := func(t *testing.T) (chainSuppressionPort, *gatewaycircuit.LocalSuppressionStore) {
		t.Helper()
		store := gatewaycircuit.NewLocalSuppressionStore(gatewaycircuit.LocalSuppressionStoreOptions{})
		return chainSuppressionPort{
			store:  store,
			waiter: gatewaycircuit.NewPreAuthRecoverableWait(nil, nil),
		}, store
	}

	suppressBoth := func(store *gatewaycircuit.LocalSuppressionStore) {
		store.Suppress("w1x-a", 60_000, "w1x 失败", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
		store.Suppress("w1x-b", 60_000, "w1x 失败", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
	}

	t.Run("全部屏蔽 + 预算耗尽 -> 503 完成契约", func(t *testing.T) {
		port, store := build(t)
		suppressBoth(store)
		coordinator := &w1xRouteCoordinator{}
		capture := &w1rAuditCapture{}
		resolved, completed, err := port.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
			Accounts:          []gatewaydispatch.AccountCandidate{{ID: "w1x-a"}, {ID: "w1x-b"}},
			AuditCapture:      gatewaydispatch.AuditCapture{Context: capture},
			StartedAt:         time.Now().Add(-5 * time.Second).UnixMilli(),
			ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
			RouteCoordinator:  coordinator,
		})
		if err != nil || !completed || resolved != nil {
			t.Fatalf("503 契约 = (%+v, %v, %v), want completed", resolved, completed, err)
		}
		if len(coordinator.failures) != 1 || coordinator.failures[0].FailureAttribution != "gateway_capacity" || coordinator.failures[0].StatusCode != 503 {
			t.Fatalf("完成契约错误: %+v", coordinator.failures)
		}
	})

	t.Run("完成失败错误透传", func(t *testing.T) {
		port, store := build(t)
		suppressBoth(store)
		coordinator := &w1xRouteCoordinator{err: errors.New("w1x 路由完成失败")}
		_, _, err := port.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
			Accounts:          []gatewaydispatch.AccountCandidate{{ID: "w1x-a"}, {ID: "w1x-b"}},
			StartedAt:         time.Now().Add(-5 * time.Second).UnixMilli(),
			ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
			RouteCoordinator:  coordinator,
		})
		if err == nil || !strings.Contains(err.Error(), "w1x 路由完成失败") {
			t.Fatalf("CompleteFailure 错误必须透传: %v", err)
		}
	})
}

func TestW1XDispatchSuppressionWaiterArms(t *testing.T) {
	ctx := context.Background()
	waiter := chainDispatchSuppressionWaiter{wait: gatewaycircuit.NewPreAuthRecoverableWait(nil, nil)}

	// 刷新样本带 retryAfter 且未全部屏蔽：就绪退出并返回样本。
	retryAfter := int64(1_500)
	state, err := waiter.WaitForState(ctx, gatewaydispatch.SuppressionWaitInput{
		ScopeKey:  "w1x::scope",
		Reason:    gatewaycircuit.LocalAccountSuppressionWaitReason,
		MaxWaitMs: 50,
		Refresh: func(context.Context) (gatewaydispatch.SuppressionFilterResult, error) {
			return gatewaydispatch.SuppressionFilterResult{
				Accounts:         []gatewaydispatch.AccountCandidate{{ID: "w1x-live"}},
				NextRetryAfterMs: &retryAfter,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("WaitForState(retry 样本): %v", err)
	}
	if len(state.Accounts) != 1 || state.Accounts[0].ID != "w1x-live" {
		t.Fatalf("等待状态 = %+v, want 刷新样本", state)
	}

	// 刷新错误必须透传。
	sentinel := errors.New("w1x 刷新失败")
	if _, err := waiter.WaitForState(ctx, gatewaydispatch.SuppressionWaitInput{
		ScopeKey:  "w1x::scope",
		Reason:    gatewaycircuit.LocalAccountSuppressionWaitReason,
		MaxWaitMs: 50,
		Refresh: func(context.Context) (gatewaydispatch.SuppressionFilterResult, error) {
			return gatewaydispatch.SuppressionFilterResult{}, sentinel
		},
	}); !errors.Is(err, sentinel) {
		t.Fatalf("刷新错误必须透传: %v", err)
	}
}

func TestW1XHotQualityLifecycleMarkFirstByte(t *testing.T) {
	gatewayhotquality.ResetGatewayHotQualityRuntimeForTest()
	t.Cleanup(gatewayhotquality.ResetGatewayHotQualityRuntimeForTest)
	runtime, err := gatewayhotquality.GetGatewayHotQualityRuntime(context.Background(), gatewayhotquality.RuntimeDriverConfig{
		RuntimeMode:        "standalone",
		RuntimeStateDriver: "memory",
	})
	if err != nil {
		t.Fatalf("create hot quality runtime: %v", err)
	}
	factory := newChainHotQualityLifecycleFactory(runtime)
	lifecycle := factory(gatewaydispatch.HotQualityLifecycleInput{
		AttemptID:   "att-w1x",
		AccountID:   "w1x-acc",
		RequestLane: "text",
		Model:       "gpt-w1x",
	})
	if lifecycle == nil {
		t.Fatal("完整输入必须挂载生命周期")
	}
	lifecycle.MarkFirstByte(nil)
	lifecycle.MarkFirstByte(w1xFloat64Ptr(233))
	lifecycle.RecordTerminal(context.Background(), gatewaydispatch.HotQualityTerminal{OutcomeClass: gatewaydispatch.HotQualityOutcomeUnknown})
}

func w1xFloat64Ptr(value float64) *float64 { return &value }

func TestW1XQuotaDBServiceBridges(t *testing.T) {
	ctx := context.Background()
	statsDB := w1xOpenBareSQLite(t, "w1x-quotadb-stats.sqlite3")
	businessDB := w1xOpenBareSQLite(t, "w1x-quotadb-business.sqlite3")
	statsStore, err := gatewayquota.NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatalf("create stats store: %v", err)
	}
	snapshot, err := gatewayquota.NewSnapshotCache(gatewayquota.Modes{}, nil, time.Now, nil)
	if err != nil {
		t.Fatalf("create snapshot cache: %v", err)
	}
	timezone := settingsTimezoneProvider{read: func(string) (string, error) { return "UTC", nil }}
	apiKeyQuota, err := gatewayquota.NewAPIKeyQuotaService(gatewayquota.APIKeyQuotaConfig{
		Modes: gatewayquota.Modes{}, Stats: statsStore, Timezone: timezone, Snapshot: snapshot, Now: time.Now,
	})
	if err != nil {
		t.Fatalf("create api-key quota service: %v", err)
	}
	authzQuota, err := gatewayquota.NewAuthorizationQuotaService(gatewayquota.AuthorizationQuotaConfig{
		Modes: gatewayquota.Modes{}, Business: businessDB, Stats: statsStore, Timezone: timezone, Snapshot: snapshot, Now: time.Now,
	})
	if err != nil {
		t.Fatalf("create authz quota service: %v", err)
	}
	service := newChainQuotaDBService(apiKeyQuota, authzQuota)

	// 无限额配置的 API Key：短路上直接放行（桥接方法体已执行）。
	apiKeyRow := gatewayquota.APIKeyRow{ID: "w1x-key", SystemAccountID: "sys_owner"}
	decision, err := service.CheckAPIKeyQuota(ctx, apiKeyRow)
	if err != nil || !decision.Allowed {
		t.Fatalf("无限额 CheckAPIKeyQuota = (%+v, %v), want 放行", decision, err)
	}
	// 空库（无任何表）上，其余三条桥接读取确定性地把 SQL 错误带出。
	if _, err := service.ReadAPIKeyQuotaCosts(ctx, apiKeyRow); err == nil {
		t.Fatal("ReadAPIKeyQuotaCosts 在空库上必须报错")
	}
	if _, err := service.CheckAuthorizationQuota(ctx, "w1x-group-authz", "w1x-account-authz"); err == nil {
		t.Fatal("CheckAuthorizationQuota 在空库上必须报错")
	}
	if _, err := service.CheckAuthorizationQuotaBatch(ctx, "w1x-group-authz",
		[]gatewayquota.AccountRef{{AccountID: "w1x-acc", AccountAuthorizationID: "w1x-account-authz"}}); err == nil {
		t.Fatal("CheckAuthorizationQuotaBatch 在空库上必须报错")
	}
}

// ---------------------------------------------------------------------------
// 5. chain_runtime.go
// ---------------------------------------------------------------------------

func TestW1XIPStatsTimezoneReaderArms(t *testing.T) {
	failing := ipstatsTimezone(func(string) (string, error) { return "", errors.New("w1x 设置读取失败") })
	if _, err := failing(context.Background()); err == nil {
		t.Fatal("设置读取错误必须透传")
	}
	empty := ipstatsTimezone(func(string) (string, error) { return "  ", nil })
	value, err := empty(context.Background())
	if err != nil || value != "UTC" {
		t.Fatalf("空时区 = (%q, %v), want (UTC, nil)", value, err)
	}
	configured := ipstatsTimezone(func(string) (string, error) { return "Asia/Shanghai", nil })
	value, err = configured(context.Background())
	if err != nil || value != "Asia/Shanghai" {
		t.Fatalf("配置时区 = (%q, %v)", value, err)
	}
}

func TestW1XComposeChainRuntimeErrorArms(t *testing.T) {
	settingValue := SettingValueFunc(func(string) (string, error) { return "UTC", nil })

	// composition 缺业务库：读模型构造失败。
	if _, err := composeChainRuntimeServices(&composition{}, runtimeConfig{DispatchAccountCandidateLimit: 20}, settingValue); err == nil ||
		!strings.Contains(err.Error(), "sql read models") {
		t.Fatalf("缺业务库必须报读模型错误: %v", err)
	}

	// 候选上限越界：选择器构造失败。
	db := w1xOpenBareSQLite(t, "w1x-compose-limit.sqlite3")
	composed := &composition{db: db, statsDB: db}
	if _, err := composeChainRuntimeServices(composed, runtimeConfig{DispatchAccountCandidateLimit: 0}, settingValue); err == nil ||
		!strings.Contains(err.Error(), "accounts selector") {
		t.Fatalf("候选上限越界必须报选择器错误: %v", err)
	}

	// runtime state redis 驱动 + 空 namespace：state 前缀构造失败于错误电路。
	server := miniredis.RunT(t)
	if _, err := composeChainRuntimeServices(composed, runtimeConfig{
		DispatchAccountCandidateLimit: 20,
		RuntimeStateDriver:            "redis",
		RedisStateURL:                 "redis://" + server.Addr(),
		RedisNamespace:                "   ",
	}, settingValue); err == nil || !strings.Contains(err.Error(), "error circuit") {
		t.Fatalf("redis 驱动空 namespace 必须报错误电路失败: %v", err)
	}

	// stats 库缺失：client-ip 策略源构造失败。
	businessOnly := &composition{db: db}
	if _, err := composeChainRuntimeServices(businessOnly, runtimeConfig{DispatchAccountCandidateLimit: 20}, settingValue); err == nil ||
		!strings.Contains(err.Error(), "policy source") {
		t.Fatalf("缺 stats 库必须报策略源错误: %v", err)
	}

	// 守卫分支：composition / 设置读取器缺失。
	if _, err := composeChainRuntimeServices(nil, runtimeConfig{}, settingValue); err == nil {
		t.Fatal("缺 composition 必须报错")
	}
	if _, err := composeChainRuntimeServices(composed, runtimeConfig{}, nil); err == nil {
		t.Fatal("缺设置读取器必须报错")
	}
}

// ---------------------------------------------------------------------------
// 6. 补充臂（第二批）：错误注入 fake 与可达错误分支
// ---------------------------------------------------------------------------

// w1xFailingAvoidanceStore 是 gatewayaccounteffects.PolicyAvoidanceStateStore
// 的恒错 fake（GetJSON/SetJSON 均失败，注入避让状态读取错误臂）。
type w1xFailingAvoidanceStore struct{}

func (w1xFailingAvoidanceStore) GetJSON(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("w1x 避让状态读取失败")
}

func (w1xFailingAvoidanceStore) SetJSON(context.Context, string, any, int64) error {
	return errors.New("w1x 避让状态写入失败")
}

func TestW1XConfiguredPolicyAvoidanceStoreErrorArms(t *testing.T) {
	ctx := context.Background()
	service := gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(w1xFailingAvoidanceStore{}, nil, nil, nil)
	accounts := []gatewaydispatch.AccountCandidate{{ID: "w1x-err", AccountAccessType: "owner"}}

	// filterConfiguredPolicyAvoidances：状态读取错误透传。
	suppression := &chainConfiguredPolicyAvoidanceSuppression{
		inner:     chainSuppressionPort{},
		avoidance: service,
	}
	if _, err := suppression.filterConfiguredPolicyAvoidances(ctx, accounts); err == nil {
		t.Fatal("避让状态读取失败必须报错")
	}

	// FilterAsync：同样的读取错误从装饰器向上传播。
	if _, err := suppression.FilterAsync(ctx, accounts, gatewaydispatch.SuppressionFilterOptions{}); err == nil {
		t.Fatal("FilterAsync 必须透传避让状态读取错误")
	}

	// ResolveLocalSuppressionFilter：同样的读取错误从 preflight 向上传播。
	if _, _, err := suppression.ResolveLocalSuppressionFilter(ctx, gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts: accounts,
	}); err == nil {
		t.Fatal("ResolveLocalSuppressionFilter 必须透传避让状态读取错误")
	}
}

// w1xInnerResolveError 是内层 SuppressionPort 的错误 fake（仅解析阶段失败）。
type w1xInnerResolveError struct{}

func (w1xInnerResolveError) FilterAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ gatewaydispatch.SuppressionFilterOptions) (gatewaydispatch.SuppressionFilterResult, error) {
	return gatewaydispatch.SuppressionFilterResult{Accounts: accounts}, nil
}

func (w1xInnerResolveError) ResolveLocalSuppressionFilter(context.Context, gatewaydispatch.LocalSuppressionPreflightInput) (*gatewaydispatch.SuppressionFilterResult, bool, error) {
	return nil, false, errors.New("w1x 内层解析失败")
}

func TestW1XConfiguredPolicyInnerResolveErrorArm(t *testing.T) {
	suppression := &chainConfiguredPolicyAvoidanceSuppression{
		inner:     w1xInnerResolveError{},
		avoidance: gatewayaccounteffects.NewConfiguredPolicyAvoidanceService(nil, nil, nil, nil),
	}
	resolved, completed, err := suppression.ResolveLocalSuppressionFilter(context.Background(), gatewaydispatch.LocalSuppressionPreflightInput{
		Accounts: []gatewaydispatch.AccountCandidate{{ID: "w1x-clean", AccountAccessType: "owner"}},
	})
	if err == nil || resolved != nil || completed {
		t.Fatalf("内层解析错误必须透传: (%+v, %v, %v)", resolved, completed, err)
	}
}

func TestW1XGuardTransientStoreFactoryErrorArm(t *testing.T) {
	factory := func() (gatewayaccounteffects.AccountApiKeyTransientStateStore, error) {
		return nil, errors.New("w1x 瞬态存储构造失败")
	}
	guard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
		gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "redis"},
		gatewayaccounteffects.SystemClock{}, nil, factory)
	port := newChainRuntimeCachePort(nil)
	port.guard = guard
	if _, err := port.LoadApiKeyTransientStatesForDispatch(context.Background(), "w1x-acc", []string{"fp"}); err == nil {
		t.Fatal("瞬态存储构造失败必须透传")
	}
}

func TestW1XGroupBindingOrdererRedisCounterErrorArm(t *testing.T) {
	// redis 驱动 + round-robin + 不可达计数器：排序错误必须透传。
	selector := gatewayrouting.NewAPIKeyGroupRouteSelector("redis",
		gatewayrouting.NewRedisRouteStateCounter("redis://127.0.0.1:1"), "redis://127.0.0.1:1")
	orderer := newChainGroupBindingOrderer(selector)
	apiKey := gatewayruntimecache.GatewayAPIKeyRow{
		ID:                "key-w1x",
		RouteStrategyMode: gatewayrouting.RouteStrategyModeRoundRobin,
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "b1", GroupID: "grp-1", Status: "active", GroupEnabled: 1, Weight: 1},
			{ID: "b2", GroupID: "grp-2", Status: "active", GroupEnabled: 1, Weight: 1},
		},
	}
	if _, err := orderer.OrderAPIKeyGroupBindings(context.Background(), apiKey); err == nil {
		t.Fatal("轮转计数器不可达必须报错")
	}
}

func TestW1XCanScheduleAndPhysicalFallbackArms(t *testing.T) {
	// canScheduleChainAuthorizedAccount 全分支。
	cases := []struct {
		name           string
		access         *chainAccountAccess
		authorizations map[string]*chainAuthorizationRow
		rowID          string
		want           bool
	}{
		{"owner 直通", &chainAccountAccess{accountAccessType: chainAccountAccessOwner}, nil, "", true},
		{"授权组直通", &chainAccountAccess{accountAccessType: chainAccountAccessGroupAuthorized}, nil, "", true},
		{"无授权 ID", &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized}, nil, "", false},
		{"预取缺失", &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized,
			accountAuthorizationID: strPtr("authz-x")}, map[string]*chainAuthorizationRow{}, "acc-1", false},
		{"预取命中", &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized,
			accountAuthorizationID: strPtr("authz-1")},
			map[string]*chainAuthorizationRow{"authz-1": {id: "authz-1"}}, "acc-1", true},
		{"row.ID 回退命中", &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized,
			accountAuthorizationID: strPtr("authz-acc")},
			map[string]*chainAuthorizationRow{"acc-1": {id: "authz-acc"}}, "acc-1", true},
		{"无预取 map", &chainAccountAccess{accountAccessType: chainAccountAccessAuthorized,
			accountAuthorizationID: strPtr("authz-1")}, nil, "acc-1", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := canScheduleChainAuthorizedAccount(&chainCandidateRow{ID: testCase.rowID}, testCase.authorizations, testCase.access)
			if got != testCase.want {
				t.Fatalf("canSchedule = %v, want %v", got, testCase.want)
			}
		})
	}

	// chainAccountAvailableForSelection：物理门拒绝短路（不触资源门）。
	available, err := chainAccountAvailableForSelection(&chainCandidateRow{Schedulable: 0, Status: "active"},
		"2026-01-01T00:00:00.000Z", false)
	if err != nil || available {
		t.Fatalf("物理门拒绝 = (%v, %v), want (false, nil)", available, err)
	}
}

func TestW1XResolveGroupAccessLocalTypeErrorArm(t *testing.T) {
	fixture, _ := w1xSelectorAuthzFixture(t)
	w1xSeedAuthzRow(t, fixture.db, "group", "w1x-authz", "w1x-grp", "sys_other")
	if _, err := fixture.db.Exec(`INSERT INTO group_authorization_settings VALUES ('w1x-authz', 'sys_other', 'w1x-grp', 1, 'bogus', NULL)`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if _, err := fixture.resolveGroupAccess(context.Background(), "w1x-grp", "sys_other"); err == nil {
		t.Fatal("本地分组类型非法必须报错")
	}
}

func TestW1XOrderChainCandidateRowsNameTail(t *testing.T) {
	// 同桶内名称不同：zh collator 参与排序。
	eligible := w1nEligible(
		&chainCandidateRow{ID: "w1x-b", Name: "乙", Priority: 5},
		&chainCandidateRow{ID: "w1x-a", Name: "甲", Priority: 5},
	)
	ordered := orderChainCandidateRowsForDispatch(eligible, nil)
	if ordered[0].row.Name != "甲" {
		t.Fatalf("同桶必须按名称排序: first=%s", ordered[0].row.Name)
	}
}

func TestW1XHydrationLimitBreakAndBadCooldownError(t *testing.T) {
	ctx := context.Background()

	t.Run("final limit 触发批量中断", func(t *testing.T) {
		db := seedChainSelectorDB(t)
		t.Cleanup(func() { _ = db.Close() })
		sealed, sealErr := accounts.EncryptJSON("secret", map[string]any{"api_key": "sk-w1x-limit"})
		if sealErr != nil {
			t.Fatalf("encrypt credentials: %v", sealErr)
		}
		if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id IN ('a-limit-acc', 'b-limit-acc', 'c-limit-acc', 'd-limit-acc')`, sealed); err != nil {
			t.Fatalf("bind credentials: %v", err)
		}
		selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 2)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}
		result, err := selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{})
		if err != nil {
			t.Fatalf("final limit 读取: %v", err)
		}
		if len(result.Accounts) != 2 || result.Diagnostics == nil || result.Diagnostics.FinalAccountCount != 2 {
			t.Fatalf("final limit 必须截断水合: %+v", result.Diagnostics)
		}
	})

	t.Run("includeUnavailable 下非法冷却进入水合并报错", func(t *testing.T) {
		db := seedChainSelectorDB(t)
		t.Cleanup(func() { _ = db.Close() })
		sealed, sealErr := accounts.EncryptJSON("secret", map[string]any{"api_key": "sk-w1x-cool"})
		if sealErr != nil {
			t.Fatalf("encrypt credentials: %v", sealErr)
		}
		for _, statement := range []string{
			`UPDATE accounts SET status = 'rate_limited', cooldown_until = 'bad-date' WHERE id = 'a-limit-acc'`,
			`UPDATE accounts SET credentials_encrypted = '` + sealed + `' WHERE id = 'a-limit-acc'`,
			`UPDATE group_accounts SET enabled = 0 WHERE account_id <> 'a-limit-acc'`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("seed %q: %v", statement[:40], err)
			}
		}
		selector, err := newChainAccountsSelectorWithStats(db, db, false, "secret", time.Now, 20)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}
		_, err = selector.ListOpenAIAccountsForGroupResult(ctx, "grp-limit", "sys_admin",
			gatewayruntimecache.OpenAIAccountsForGroupOptions{
				PreResolvedGroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{
					GroupOwnerSystemAccountID: "sys_admin",
					ProviderCode:              "openai",
					GroupAccessType:           gatewayruntimecache.GroupAccessTypeOwner,
				},
				IncludeUnavailable: true,
			})
		if err == nil || !strings.Contains(err.Error(), "cooldownUntil") {
			t.Fatalf("非法冷却时间必须在水合阶段报错: %v", err)
		}
	})
}
