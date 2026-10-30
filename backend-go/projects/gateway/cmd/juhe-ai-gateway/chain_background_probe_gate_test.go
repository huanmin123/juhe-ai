package main

// 后台自动探测总开关（PLAN-20261030T062141000Z W2）请求失败二次确认投递端
// 守卫：chainProbeRequestOutboxWriter 的生效开关读取（实例行跟随来源、缺失
// 行 fail-open 交给 jobs 消费端兜底）与不投递契约（rejected 不写行、不消耗
// 请求级标记）。pgtestkit 隔离 fresh 库 + accounts 最小列夹具（与生产查询
// 只触达的列集合同形）。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// newBackgroundProbeGateFixture 在隔离库上构造 writer + accounts 最小契约列
// （投递端开关查询只触达 id/deleted_at/实例两列/background_probe_enabled）。
func newBackgroundProbeGateFixture(t *testing.T, deadlineMS int64) *healthOutboxFixture {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS juhe_business.accounts (
			id text PRIMARY KEY,
			deleted_at text,
			authorization_instance_source_account_id text,
			authorization_instance_authorization_id text,
			background_probe_enabled integer NOT NULL DEFAULT 1 CHECK (background_probe_enabled IN (0, 1)))`); err != nil {
			return err
		}
		return nil
	})
	writer := newChainProbeRequestOutboxWriter(db, deadlineMS)
	// outbox 表显式预建：关闭账户的派发在 ensureSchema 之前即被守卫拒绝，
	// 惰性建表不会发生，行读取断言需要表已存在。
	if err := writer.ensureSchema(context.Background()); err != nil {
		t.Fatalf("ensure outbox schema: %v", err)
	}
	return &healthOutboxFixture{db: db, writer: writer}
}

func (f *healthOutboxFixture) seedAccount(t *testing.T, id string, source any, enabled int) {
	t.Helper()
	// 实例行（source 非空）必须携带非空授权 ID，生效开关 CASE 才会取来源
	// 账户分支（与生产实例行同形）。
	authorization := any(nil)
	if source != nil {
		authorization = id + "-auth"
	}
	if _, err := f.db.Exec(`INSERT INTO juhe_business.accounts (id, authorization_instance_source_account_id, authorization_instance_authorization_id, background_probe_enabled)
		VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO UPDATE SET authorization_instance_source_account_id = EXCLUDED.authorization_instance_source_account_id, authorization_instance_authorization_id = EXCLUDED.authorization_instance_authorization_id, background_probe_enabled = EXCLUDED.background_probe_enabled`, id, source, authorization, enabled); err != nil {
		t.Fatal(err)
	}
}

// TestChainProbeRequestOutboxBackgroundProbeGate：flag=0 不写行且按
// probe_skipped_disabled 拒绝；实例行生效值跟随来源；账户缺失 fail-open
// （jobs 消费端守卫兜底，既有派发契约不变）。
func TestChainProbeRequestOutboxBackgroundProbeGate(t *testing.T) {
	fixture := newBackgroundProbeGateFixture(t, 65_000)
	ctx := context.Background()
	fixture.seedAccount(t, "acc_off", nil, 0)
	fixture.seedAccount(t, "acc_src", nil, 0)
	fixture.seedAccount(t, "acc_inst", "acc_src", 1)

	// flag=0：拒绝且不写行。
	if got := fixture.writer.EnqueueProbeRequest(ctx, "acc_off", chainRequestFailureReason, "", nil); got.Outcome != gatewaycodex.HealthDispatchRejected || got.DecisionCode != probeSkippedDisabledDecision {
		t.Fatalf("flag=0 dispatch outcome = %#v", got)
	}
	if rows := fixture.count(t); rows != 0 {
		t.Fatalf("disabled account must not write outbox rows, rows=%d", rows)
	}
	// 实例行显式 1 但来源 0 → 跟随来源不投递。
	if got := fixture.writer.EnqueueProbeRequest(ctx, "acc_inst", chainRequestFailureReason, "", nil); got.Outcome != gatewaycodex.HealthDispatchRejected || got.DecisionCode != probeSkippedDisabledDecision {
		t.Fatalf("instance with disabled source outcome = %#v", got)
	}
	if rows := fixture.count(t); rows != 0 {
		t.Fatalf("instance rows leaked, rows=%d", rows)
	}
	// 来源翻回 1 → 实例生效值恢复，照常投递。
	if _, err := fixture.db.Exec(`UPDATE juhe_business.accounts SET background_probe_enabled = 1 WHERE id = 'acc_src'`); err != nil {
		t.Fatal(err)
	}
	if got := fixture.writer.EnqueueProbeRequest(ctx, "acc_inst", chainRequestFailureReason, "", nil); got.Outcome != gatewaycodex.HealthDispatchQueued {
		t.Fatalf("re-enabled instance outcome = %#v", got)
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("enabled instance must write one row, rows=%d", rows)
	}
	// 账户行缺失（及表缺失的降级形态）：fail-open 保持既有派发契约。
	if got := fixture.writer.EnqueueProbeRequest(ctx, "acc_missing", chainRequestFailureReason, "", nil); got.Outcome != gatewaycodex.HealthDispatchQueued {
		t.Fatalf("missing account outcome = %#v", got)
	}
	if rows := fixture.count(t); rows != 2 {
		t.Fatalf("missing account must fall through, rows=%d", rows)
	}
}

// TestChainRequestFailureDispatcherSkipsDisabledAccount：请求级门禁对关闭
// 账户静默返回 false，不消耗标记（后续失败仍可重试判定），不写行。
func TestChainRequestFailureDispatcherSkipsDisabledAccount(t *testing.T) {
	fixture := newBackgroundProbeGateFixture(t, 65_000)
	fixture.seedAccount(t, "acc_off", nil, 0)
	dispatcher := fixture.dispatcher()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/chat/completions", nil))

	if dispatcher.DispatchRequestFailureAccountHealthCheck(req, gatewayTrafficSource, "acc_off") {
		t.Fatal("disabled account must not dispatch")
	}
	if req.RequestFailureHealthCheckDispatched() {
		t.Fatal("skipped dispatch must not mark the request")
	}
	if rows := fixture.count(t); rows != 0 {
		t.Fatalf("rows = %d want 0", rows)
	}

	// 对照：默认开启（无行 = DEFAULT 1）照常派发并标记。
	if !dispatcher.DispatchRequestFailureAccountHealthCheck(req, gatewayTrafficSource, "acc_default_on") {
		t.Fatal("enabled account must dispatch")
	}
	if !req.RequestFailureHealthCheckDispatched() {
		t.Fatal("queued dispatch must mark the request")
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("rows = %d want 1", rows)
	}
}
