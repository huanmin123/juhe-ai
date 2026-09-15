package authorization

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

var w7aErrBoom = errors.New("w7a boom")

// 可配置用量的 UsagePort。
type w7aUsage struct {
	requests int64
	err      error
	calls    int
}

func (u *w7aUsage) Usage(context.Context, Scope) (Usage, error) {
	u.calls++
	return Usage{Requests: u.requests}, u.err
}

// ---------------------------------------------------------------------------
// 构造函数与方言
// ---------------------------------------------------------------------------

func TestW7AConstructorAndDialectArms(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{}, nil); err == nil {
		t.Fatal("nil db must be rejected")
	}
	db, err := sql.Open("sqlite", "file:w7a-authz-ctor?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := New(db, "oracle", "", OwnerGate{}, nil); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
	pg := &Store{mode: Postgres, schema: "juhe_business"}
	if got := pg.table("resource_authorizations"); got != "juhe_business.resource_authorizations" {
		t.Fatalf("pg table=%q", got)
	}
	bare := &Store{mode: Postgres}
	if got := bare.table("resource_authorizations"); got != "resource_authorizations" {
		t.Fatalf("empty schema table=%q", got)
	}
	if got := pg.bind("id=? AND status=?"); got != "id=$1 AND status=$2" {
		t.Fatalf("pg bind=%q", got)
	}
	if got := (&Store{}).bind("id=?"); got != "id=?" {
		t.Fatal("sqlite bind changed query")
	}
}

// ---------------------------------------------------------------------------
// CheckQuota / evaluate 全分支
// ---------------------------------------------------------------------------

func TestW7ACheckQuotaMatrix(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	now := store.now().UTC()
	seed := func(id, limits, status string, expires string) {
		var expiry any
		if expires != "" {
			expiry = expires
		}
		if _, err := db.Exec(`INSERT INTO resource_authorizations(id,limits_json,status,expires_at) VALUES (?,?,?,?)`, id, limits, status, expiry); err != nil {
			t.Fatal(err)
		}
	}
	future := now.Add(time.Hour).Format(time.RFC3339Nano)
	past := now.Add(-time.Hour).Format(time.RFC3339Nano)
	seed("gone", `{"hourly":{"requests":5}}`, "revoked", "")
	seed("expired", `{"hourly":{"requests":5}}`, "active", past)
	seed("live", `{"hourly":{"requests":5}}`, "active", future)
	seed("empty", `{}`, "active", "")
	seed("bad", `not-json`, "active", "")
	seed("zero", `{"daily":{"requests":0}}`, "active", "")
	seed("daily", `{"daily":{"requests":5}}`, "active", "")
	usage := &w7aUsage{requests: 4}
	store.usage = usage
	// 未知授权：直接放行。
	decision, err := store.CheckQuota(ctx, "unknown", "unknown2")
	if err != nil || !decision.Allowed {
		t.Fatalf("unknown decision=%+v err=%v", decision, err)
	}
	// 已撤销 / 已过期：跳过。
	if d, err := store.CheckQuota(ctx, "gone", ""); err != nil || !d.Allowed {
		t.Fatalf("revoked=%+v err=%v", d, err)
	}
	if d, err := store.CheckQuota(ctx, "expired", ""); err != nil || !d.Allowed {
		t.Fatalf("expired=%+v err=%v", d, err)
	}
	// 未超限与空限。
	if d, err := store.CheckQuota(ctx, "live", ""); err != nil || !d.Allowed {
		t.Fatalf("under=%+v err=%v", d, err)
	}
	if d, err := store.CheckQuota(ctx, "empty", ""); err != nil || !d.Allowed {
		t.Fatalf("empty limits=%+v err=%v", d, err)
	}
	if d, err := store.CheckQuota(ctx, "zero", ""); err != nil || !d.Allowed {
		t.Fatalf("zero window=%+v err=%v", d, err)
	}
	// 超限：hourly 与 daily 各自违规。
	usage.requests = 9
	if d, err := store.CheckQuota(ctx, "live", ""); err != nil || d.Allowed || len(d.Violations) != 1 || d.Violations[0] != "hourly" {
		t.Fatalf("hourly violation=%+v err=%v", d, err)
	}
	if d, err := store.CheckQuota(ctx, "daily", ""); err != nil || d.Allowed || d.Violations[0] != "daily" {
		t.Fatalf("daily violation=%+v err=%v", d, err)
	}
	// usage 上报失败必须上抛。
	usage.err = w7aErrBoom
	if _, err := store.CheckQuota(ctx, "live", ""); !errors.Is(err, w7aErrBoom) {
		t.Fatalf("usage error=%v", err)
	}
	// 无 usage owner 且配额启用：fail closed。
	store.usage = nil
	if _, err := store.CheckQuota(ctx, "live", ""); err == nil {
		t.Fatal("missing usage owner must fail")
	}
	// 非法 limits JSON。
	if _, _, err := store.evaluate(ctx, "bad", `not-json`, now); err == nil {
		t.Fatal("invalid limits json must fail")
	}
	// 批量：去重与多账户。
	usage2 := &w7aUsage{requests: 1}
	store.usage = usage2
	batch, err := store.CheckQuotaBatch(ctx, QuotaRequest{
		GroupAuthorizationID: "live",
		Accounts:             []AccountScope{{AccountID: "a", AuthorizationID: "live"}, {AccountID: "b", AuthorizationID: " empty "}},
	})
	if err != nil || len(batch) != 2 || !batch[0].Allowed || !batch[1].Allowed {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	// 群组 + 账户双 ID 组合评估。
	groupLimits := `{"hourly":{"requests":1}}`
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,limits_json,status) VALUES ('grp',?, 'active')`, groupLimits); err != nil {
		t.Fatal(err)
	}
	usage3 := &w7aUsage{requests: 9}
	store.usage = usage3
	if d, err := store.CheckQuota(ctx, "grp", "live"); err != nil || d.Allowed || len(d.Violations) != 2 {
		t.Fatalf("combined=%+v err=%v", d, err)
	}
	// 两个授权各只有 hourly 窗口：共 2 次用量调用。
	if usage3.calls != 2 {
		t.Fatalf("expected 2 usage calls, got %d", usage3.calls)
	}
}

// ---------------------------------------------------------------------------
// ExpireDue / SyncAvailability 剩余分支
// ---------------------------------------------------------------------------

func TestW7AExpireDueArms(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, err := store.ExpireDue(ctx, 10); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate=%v", err)
	}
	store.gate = OwnerGate{true, true, true}
	// limit<=0 使用默认值；无到期行时零计数。
	result, err := store.ExpireDue(ctx, 0)
	if err != nil || result.GrantsExpired != 0 || result.AuthorizationsExpired != 0 {
		t.Fatalf("default result=%+v err=%v", result, err)
	}
	// 关闭数据库后查询失败必须上抛。
	broken, err := sql.Open("sqlite", "file:w7a-authz-broken?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	brokenStore, err := New(broken, SQLite, "", OwnerGate{true, true, true}, usage(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := brokenStore.ExpireDue(ctx, 10); err == nil {
		t.Fatal("closed db expire must fail")
	}
	if _, err := brokenStore.CheckQuotaBatch(ctx, QuotaRequest{}); err == nil {
		t.Fatal("closed db quota must fail")
	}
}

func TestW7ASyncAvailabilityArms(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	next := store.now().Add(time.Hour)
	// evaluator 缺失。
	if _, err := store.SyncAvailability(ctx, nil, 10); err == nil {
		t.Fatal("nil evaluator must fail")
	}
	seedAccounts := `INSERT INTO accounts(id,status,availability_schedule_json,availability_schedule_next_check_at,updated_at) VALUES`
	if _, err := db.Exec(seedAccounts + `('a1','active','noop',NULL,'u1'),('a2','disabled','noop',NULL,'u2'),('a3','active','noop',NULL,'u3'),('a4','active','noop',NULL,'u4')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_schedule_status_events(event_key,account_id,status,executed_at) VALUES ('dup','a3','x','e')`); err != nil {
		t.Fatal(err)
	}
	eval := w7aMapEval{decisions: map[string]ScheduleDecision{
		"noop": {NextCheckAt: &next},
		"on":   {NextCheckAt: &next, EventKey: "on", Status: "active"},
		"skip": {NextCheckAt: &next, EventKey: "dup", Status: "active"},
		"off":  {NextCheckAt: &next, EventKey: "off", Status: "disabled"},
		"same": {NextCheckAt: &next, EventKey: "same", Status: "disabled"},
		"bad":  {},
	}}
	// 空状态决策：全部 Unchanged。
	result, err := store.SyncAvailability(ctx, eval, 0)
	if err != nil || result.Unchanged != 4 {
		t.Fatalf("noop result=%+v err=%v", result, err)
	}
	// 激活 / 禁用 / 事件去重 / 空决策混合。
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_next_check_at=NULL, availability_schedule_json=CASE id WHEN 'a2' THEN 'on' WHEN 'a3' THEN 'skip' WHEN 'a4' THEN 'off' ELSE 'noop' END`); err != nil {
		t.Fatal(err)
	}
	result, err = store.SyncAvailability(ctx, eval, 10)
	if err != nil || result.Activated != 1 || result.Disabled != 1 || result.Skipped != 1 || result.Unchanged != 1 {
		t.Fatalf("mixed result=%+v err=%v", result, err)
	}
	// 无效调度：active → disabled，非 active 只计数。
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_next_check_at=NULL, availability_schedule_json='bad'`); err != nil {
		t.Fatal(err)
	}
	result, err = store.SyncAvailability(ctx, w7aMapEval{errByRaw: map[string]error{"bad": w7aErrBoom}}, 10)
	if err != nil || result.Invalid != 4 {
		t.Fatalf("invalid result=%+v err=%v", result, err)
	}
	var a1Status string
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='a1'`).Scan(&a1Status); err != nil || a1Status != "disabled" {
		t.Fatalf("invalid active account status=%s err=%v", a1Status, err)
	}
	// 相同状态决策计为 Unchanged。
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_next_check_at=NULL, availability_schedule_json=CASE id WHEN 'a1' THEN 'same' ELSE 'noop' END`); err != nil {
		t.Fatal(err)
	}
	result, err = store.SyncAvailability(ctx, eval, 10)
	if err != nil || result.Unchanged != 4 {
		t.Fatalf("same status result=%+v err=%v", result, err)
	}
}

// w7aMapEval 按 schedule JSON 分派决策或错误。
type w7aMapEval struct {
	decisions map[string]ScheduleDecision
	errByRaw  map[string]error
}

func (e w7aMapEval) Evaluate(_ context.Context, raw string, _ time.Time) (ScheduleDecision, error) {
	if e.errByRaw != nil && e.errByRaw[raw] != nil {
		return ScheduleDecision{}, e.errByRaw[raw]
	}
	return e.decisions[raw], nil
}

func TestW7AUpdateNextAndNullTimeArms(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO accounts(id,status,availability_schedule_json,availability_schedule_next_check_at,updated_at) VALUES ('u1','active','{}',NULL,'u')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	next := store.now().Add(time.Hour)
	if err := store.updateNext(ctx, tx, "u1", "u", next); err != nil {
		t.Fatal(err)
	}
	if err := store.updateNext(ctx, tx, "u1", "stale", next); !errors.Is(err, ErrCAS) {
		t.Fatalf("stale updateNext=%v", err)
	}
	if err := store.updateNext(ctx, tx, "ghost", "x", next); !errors.Is(err, ErrCAS) {
		t.Fatalf("missing updateNext=%v", err)
	}
	// sqlNullTime 两臂。
	if sqlNullTime(nil) != nil {
		t.Fatal("nil time must be nil")
	}
	if sqlNullTime(&next) == nil {
		t.Fatal("set time must render")
	}
	// sentinel 文案保持稳定。
	if !strings.Contains(ErrCAS.Error(), "changed") {
		t.Fatal("sentinel text")
	}
}
