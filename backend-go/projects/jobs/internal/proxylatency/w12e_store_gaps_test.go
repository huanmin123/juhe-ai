package proxylatency

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// w12e_store_gaps_test.go 补齐 Store 剩余错误臂：EnsureSchema/契约检查、
// lease 与 outcome 写入链的逐阶段失败注入、Admit/Load/List 的 tail 分支。
// 复用 wf 录制驱动（failQuery/failExec/beginErr/commitErr），失败按 SQL
// 子串注入，不依赖真实 PostgreSQL。

const w12eOwnerRow = "FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key"
const w12eProxyLeaseRow = "FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1 FOR UPDATE"

// w12eScriptLeases 登记一条活跃 owner/proxy lease 行，供 verify*Tx 通过。
func w12eScriptLeases(rec *wfRecorder, owner OwnerLease, proxy ProxyLease) {
	rec.script(w12eOwnerRow, []string{"owner", "token", "until"}, [][]driver.Value{{owner.OwnerID, owner.FenceToken, time.Now().Add(time.Minute)}})
	rec.script(w12eProxyLeaseRow, []string{"owner", "token", "until"}, [][]driver.Value{{proxy.OwnerID, proxy.FenceToken, time.Now().Add(time.Minute)}})
}

// TestW12EOpenStoreSQLiteArms：SQLite OpenStore 缺路径错误臂。
func TestW12EOpenStoreSQLiteArms(t *testing.T) {
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite}); err == nil || !strings.Contains(err.Error(), "缺少数据库路径") {
		t.Fatalf("空路径应报错: %v", err)
	}
	if _, err := OpenStore(StoreConfig{Mode: "weird"}); err == nil {
		t.Fatalf("未知模式应报错")
	}
}

// TestW12EStoreNilGuards：未初始化 Store 的防御臂。
func TestW12EStoreNilGuards(t *testing.T) {
	empty := &Store{}
	ctx := context.Background()
	if err := empty.CheckSchema(ctx); err == nil {
		t.Fatalf("CheckSchema 未初始化应报错")
	}
	if err := empty.ensureRuntimeSchema(ctx); err == nil {
		t.Fatalf("ensureRuntimeSchema 未初始化应报错")
	}
	if _, err := empty.ListCommittedOutcomes(ctx, nil, 10); err == nil {
		t.Fatalf("ListCommittedOutcomes 未初始化应报错")
	}
	if _, _, err := empty.AcquireOwnerLease(ctx, "", 0); err == nil || !strings.Contains(err.Error(), "owner lease 参数无效") {
		t.Fatalf("owner lease 参数校验: %v", err)
	}
	if _, _, err := empty.AcquireProxyLease(ctx, OwnerLease{}, "", 0); err == nil {
		t.Fatalf("proxy lease 参数校验应报错")
	}
	if err := empty.ReleaseOwnerLease(ctx, OwnerLease{}); err == nil {
		t.Fatalf("owner lease 释放参数校验应报错")
	}
	if err := empty.ReleaseProxyLease(ctx, ProxyLease{}); err == nil {
		t.Fatalf("proxy lease 释放参数校验应报错")
	}
}

// TestW12EEnsureSchemaPGArms：EnsureSchema PG 路径逐阶段失败与成功。
func TestW12EEnsureSchemaPGArms(t *testing.T) {
	ctx := context.Background()
	// BeginTx 失败。
	rec := newWFRecorder()
	rec.beginErr = errors.New("w12e begin boom")
	if err := (&Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}).EnsureSchema(ctx); err == nil {
		t.Fatalf("begin 失败应透传")
	}
	// schema 缺失（ErrNoRows）。
	rec = newWFRecorder()
	if err := (&Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}).EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "缺少外部 bootstrap") {
		t.Fatalf("无 schema 行应报错: %v", err)
	}
	// owner != currentUser。
	rec = newWFRecorder()
	rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"other", "me"}})
	if err := (&Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}).EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "owner 必须是当前 jobs role") {
		t.Fatalf("owner 不匹配应报错: %v", err)
	}
	// DDL 语句失败。
	rec = newWFRecorder()
	rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"me", "me"}})
	rec.failExec("CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_owner_leases", errors.New("w12e ddl boom"))
	if err := (&Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}).EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "初始化 proxy-latency postgres schema 失败") {
		t.Fatalf("DDL 失败应包装: %v", err)
	}
	// 成功 + commit 失败。
	rec = newWFRecorder()
	rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"me", "me"}})
	if err := (&Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}).EnsureSchema(ctx); err != nil {
		t.Fatalf("完整 schema 应成功: %v", err)
	}
	rec = newWFRecorder()
	rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"me", "me"}})
	rec.commitErr = errors.New("w12e commit boom")
	if err := (&Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}).EnsureSchema(ctx); err == nil {
		t.Fatalf("commit 失败应透传")
	}
}

// TestW12ECheckPostgresSchemaArms：运行时契约检查逐阶段失败。
func TestW12ECheckPostgresSchemaArms(t *testing.T) {
	ctx := context.Background()
	build := func() (*wfRecorder, *Store) {
		rec := newWFRecorder()
		return rec, &Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}
	}
	// BeginTx 失败。
	rec, store := build()
	rec.beginErr = errors.New("w12e begin")
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "事务失败") {
		t.Fatalf("begin 失败: %v", err)
	}
	// SET LOCAL 失败。
	rec, store = build()
	rec.failExec("statement_timeout", errors.New("w12e set local"))
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "超时失败") {
		t.Fatalf("SET LOCAL 失败: %v", err)
	}
	// 读取 owner 失败。
	rec, store = build()
	rec.failQuery("FROM pg_namespace WHERE nspname='juhe_jobs'", errors.New("w12e ns boom"))
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "读取 proxy-latency postgres schema owner 失败") {
		t.Fatalf("owner 查询失败: %v", err)
	}
	// tables 查询失败。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	rec.failQuery("FROM information_schema.tables WHERE table_schema='juhe_jobs'", errors.New("w12e tables"))
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "读取 proxy-latency postgres tables 失败") {
		t.Fatalf("tables 查询失败: %v", err)
	}
	// tables 行解码失败：脚本列数(2)与 Scan 目标数(1)不匹配。database/sql
	// 会把 bool/int64/time.Time 全部静默转换成 string，无法用值类型制造错误。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	rec.script("FROM information_schema.tables WHERE table_schema='juhe_jobs'", []string{"table_name", "extra"}, [][]driver.Value{{"t", "x"}})
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "读取 proxy-latency postgres table 名称失败") {
		t.Fatalf("tables scan 失败: %v", err)
	}
	// tables 迭代失败。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	rec.scriptRowsErr("FROM information_schema.tables WHERE table_schema='juhe_jobs'", errors.New("w12e iter"))
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "遍历 proxy-latency postgres tables 失败") {
		t.Fatalf("tables 迭代失败: %v", err)
	}
	// 列缺失（shape invalid）。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	rec.script("FROM information_schema.tables WHERE table_schema='juhe_jobs'", []string{"table_name"}, [][]driver.Value{})
	if err := store.checkPostgresSchema(ctx); err == nil {
		t.Fatalf("缺表应报错")
	}
}

// w12eScriptOwnerOK 登记通过 owner 校验的 pg_namespace 行。
func w12eScriptOwnerOK(rec *wfRecorder) {
	rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"me", "me"}})
}

// w12eScriptAllTables 登记契约要求的全部表名行（通过缺表检查）。
func w12eScriptAllTables(rec *wfRecorder) {
	rows := make([][]driver.Value, 0, len(contracts.J3AProxyLatencyTables))
	for _, name := range contracts.J3AProxyLatencyTables {
		rows = append(rows, []driver.Value{name})
	}
	rec.script("FROM information_schema.tables WHERE table_schema='juhe_jobs'", []string{"table_name"}, rows)
}

// w12eScriptAllColumns 登记契约要求的全部列定义行（通过列契约检查）。
func w12eScriptAllColumns(rec *wfRecorder) {
	var rows [][]driver.Value
	for _, table := range contracts.J3AProxyLatencyTables {
		for column, spec := range contracts.J3AProxyLatencyColumns[table] {
			nullable := "NO"
			if spec.Nullable {
				nullable = "YES"
			}
			rows = append(rows, []driver.Value{table, column, spec.DataType, spec.UdtName, nullable})
		}
	}
	rec.script("FROM information_schema.columns WHERE table_schema='juhe_jobs'", []string{"table_name", "column_name", "data_type", "udt_name", "is_nullable"}, rows)
}

// TestW12ECheckSchemaShapeArms：checkPostgresSchemaShape 列/约束解码臂。
func TestW12ECheckSchemaShapeArms(t *testing.T) {
	ctx := context.Background()
	build := func() (*wfRecorder, *Store) {
		rec := newWFRecorder()
		return rec, &Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}
	}
	// 列查询失败。
	rec, store := build()
	w12eScriptOwnerOK(rec)
	w12eScriptAllTables(rec)
	rec.failQuery("FROM information_schema.columns WHERE table_schema='juhe_jobs'", errors.New("w12e cols"))
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "读取 J3a PostgreSQL schema 列契约失败") {
		t.Fatalf("columns 查询失败: %v", err)
	}
	// 列行解码失败。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	w12eScriptAllTables(rec)
	rec.script("FROM information_schema.columns WHERE table_schema='juhe_jobs'", []string{"a", "b", "c", "d"}, [][]driver.Value{{"a", "b", "c", "d"}})
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "读取 J3a PostgreSQL schema 列定义失败") {
		t.Fatalf("columns scan 失败: %v", err)
	}
	// 列迭代失败。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	w12eScriptAllTables(rec)
	rec.scriptRowsErr("FROM information_schema.columns WHERE table_schema='juhe_jobs'", errors.New("w12e col iter"))
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "遍历 J3a PostgreSQL schema 列契约失败") {
		t.Fatalf("columns 迭代失败: %v", err)
	}
	// 约束行解码失败。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	w12eScriptAllTables(rec)
	w12eScriptAllColumns(rec)
	rec.script("FROM pg_constraint AS c", []string{"relname", "def"}, [][]driver.Value{{"one"}})
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "读取 J3a PostgreSQL schema constraint 定义失败") {
		t.Fatalf("constraint scan 失败: %v", err)
	}
	// 约束迭代失败。
	rec, store = build()
	w12eScriptOwnerOK(rec)
	w12eScriptAllTables(rec)
	w12eScriptAllColumns(rec)
	rec.scriptRowsErr("FROM pg_constraint AS c", errors.New("w12e cons iter"))
	if err := store.checkPostgresSchema(ctx); err == nil || !strings.Contains(err.Error(), "遍历 J3a PostgreSQL schema constraint 契约失败") {
		t.Fatalf("constraint 迭代失败: %v", err)
	}
}

// TestW12EEnsureRuntimeSchemaPGPreflight：PG 模式首次预检收缩逻辑。
func TestW12EEnsureRuntimeSchemaPGPreflight(t *testing.T) {
	rec := newWFRecorder()
	w12eScriptOwnerOK(rec)
	// 契约检查需要的其余查询按空结果返回 → 缺表错误，但足以覆盖
	// ready=false → schemaCheckMu → CheckSchema 的首次预检链。
	store := &Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres, postgresSchemaReady: false}
	if err := store.ensureRuntimeSchema(context.Background()); err == nil {
		t.Fatalf("空契约应报缺表错误")
	}
	if store.postgresSchemaReady {
		t.Fatalf("失败后不应置 ready")
	}
	// ready=true 直接返回。
	store.postgresSchemaReady = true
	if err := store.ensureRuntimeSchema(context.Background()); err != nil {
		t.Fatalf("ready 应跳过: %v", err)
	}
}

// TestW12ELeaseArms：owner/proxy lease 的失败与冲突臂。
func TestW12ELeaseArms(t *testing.T) {
	ctx := context.Background()
	owner := OwnerLease{OwnerID: "w12e-owner", FenceToken: 3}
	proxy := ProxyLease{ProxyID: "w12e-proxy", OwnerID: owner.OwnerID, FenceToken: 5}
	// AcquireOwnerLease PG 查询失败。
	rec := newWFRecorder()
	rec.failQuery("proxy_latency_owner_leases", errors.New("w12e upsert boom"))
	store := wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, _, err := store.AcquireOwnerLease(ctx, owner.OwnerID, time.Minute); err == nil {
		t.Fatalf("owner upsert 失败应透传")
	}
	// AcquireProxyLease begin 失败。
	rec = newWFRecorder()
	rec.beginErr = errors.New("w12e begin")
	store = wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, _, err := store.AcquireProxyLease(ctx, owner, proxy.ProxyID, time.Minute); err == nil {
		t.Fatalf("begin 失败应透传")
	}
	// ErrNoRows + commit 失败（owner lease 校验先通过）。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.commitErr = errors.New("w12e commit")
	store = wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, _, err := store.AcquireProxyLease(ctx, owner, proxy.ProxyID, time.Minute); err == nil {
		t.Fatalf("no-rows + commit 失败应报 commit 错误")
	}
	// ErrNoRows + commit 成功 → 查不到归属 → ErrProxyLeaseHeld。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	store = wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, _, err := store.AcquireProxyLease(ctx, owner, proxy.ProxyID, time.Minute); !errors.Is(err, ErrProxyLeaseHeld) {
		t.Fatalf("无归属应 ErrProxyLeaseHeld: %v", err)
	}
	// upsert 查询失败。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.failQuery("INSERT INTO juhe_jobs.proxy_latency_proxy_leases", errors.New("w12e proxy upsert"))
	store = wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, _, err := store.AcquireProxyLease(ctx, owner, proxy.ProxyID, time.Minute); err == nil {
		t.Fatalf("proxy upsert 失败应透传")
	}
	// upsert 成功但 commit 失败。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.commitErr = errors.New("w12e commit2")
	rec.script("INSERT INTO juhe_jobs.proxy_latency_proxy_leases", []string{"fence_token"}, [][]driver.Value{{int64(9)}})
	store = wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, _, err := store.AcquireProxyLease(ctx, owner, proxy.ProxyID, time.Minute); err == nil {
		t.Fatalf("成功 upsert 后 commit 失败应透传")
	}
	// VerifyOwnerLease begin 失败。
	rec = newWFRecorder()
	rec.beginErr = errors.New("w12e begin3")
	store = wfNewPGStore(wfOpenRecorderDB(t, rec))
	if err := store.VerifyOwnerLease(ctx, owner); err == nil {
		t.Fatalf("VerifyOwnerLease begin 失败应透传")
	}
	// activeProxyLeaseBelongsTo PG 查询：查到行但不属于当前 owner → held。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.script("SELECT owner_id,lease_until FROM juhe_jobs.proxy_latency_proxy_leases", []string{"owner", "until"}, [][]driver.Value{{"someone-else", time.Now().Add(time.Minute)}})
	store = wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, _, err := store.AcquireProxyLease(ctx, owner, proxy.ProxyID, time.Minute); !errors.Is(err, ErrProxyLeaseHeld) {
		t.Fatalf("他人持有应 ErrProxyLeaseHeld: %v", err)
	}
}

// TestW12EAppendOutcomeArms：AppendOutcome 的冲突/校验/claim 链错误臂。
func TestW12EAppendOutcomeArms(t *testing.T) {
	ctx := context.Background()
	owner := OwnerLease{OwnerID: "w12e-owner", FenceToken: 1}
	proxy := ProxyLease{ProxyID: "w12e-proxy", OwnerID: owner.OwnerID, FenceToken: 1}
	now := time.Now().UTC().Truncate(time.Microsecond) // 与 PG canonicalPostgresTimestamp 对齐
	outcome := Outcome{
		OutcomeID: "w12e-outcome", RequestID: "j3a-w12e-req", ProxyID: proxy.ProxyID,
		ObservedAt: now, InputVersion: 1, ConfigRevision: now.Add(-time.Second).Format(time.RFC3339Nano),
		Trigger: TriggerPeriodic, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed, Items: []ItemResult{{Status: ItemPassed, HTTPStatus: 200}},
	}
	digestOf := func(value Outcome) string {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(payload)
		return hex.EncodeToString(sum[:])
	}
	// begin 失败。
	rec := newWFRecorder()
	rec.beginErr = errors.New("w12e begin")
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); err == nil {
		t.Fatalf("begin 失败应透传")
	}
	// 已存在且匹配（replay）→ commit 失败。
	rec = newWFRecorder()
	rec.commitErr = errors.New("w12e replay commit")
	matched := outcome
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,payload_digest FROM juhe_jobs.proxy_latency_outcomes",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "payload_digest"},
		[][]driver.Value{{matched.OutcomeID, matched.ProxyID, matched.InputVersion, matched.ConfigRevision, string(matched.Trigger), matched.OwnerFenceToken, matched.ProxyFenceToken, digestOf(matched)}})
	inserted, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome)
	if inserted || err == nil {
		t.Fatalf("replay + commit 失败: inserted=%v err=%v", inserted, err)
	}
	// 已存在且匹配（replay）→ 成功幂等返回 false。
	rec = newWFRecorder()
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,payload_digest FROM juhe_jobs.proxy_latency_outcomes",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "payload_digest"},
		[][]driver.Value{{matched.OutcomeID, matched.ProxyID, matched.InputVersion, matched.ConfigRevision, string(matched.Trigger), matched.OwnerFenceToken, matched.ProxyFenceToken, digestOf(matched)}})
	inserted, err = wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome)
	if inserted || err != nil {
		t.Fatalf("replay 应幂等: inserted=%v err=%v", inserted, err)
	}
	// outcomeMatches 查询失败（非 NoRows）。
	rec = newWFRecorder()
	rec.failQuery("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,payload_digest FROM juhe_jobs.proxy_latency_outcomes", errors.New("w12e matches"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); err == nil {
		t.Fatalf("matches 查询失败应透传")
	}
	// lease 与 outcome 不匹配（首插 fence 校验）。
	rec = newWFRecorder()
	badProxy := proxy
	badProxy.OwnerID = "other-owner"
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, badProxy, outcome); err == nil || !strings.Contains(err.Error(), "outcome lease 与输入不匹配") {
		t.Fatalf("fence 不匹配应报错: %v", err)
	}
	// verifyOwnerTx 失败。
	rec = newWFRecorder()
	rec.failQuery("FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key", errors.New("w12e owner lost"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); err == nil {
		t.Fatalf("owner 校验失败应透传")
	}
	// verifyProxyTx 失败。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, ProxyLease{})
	rec.failQuery(w12eProxyLeaseRow, errors.New("w12e proxy lost"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); err == nil {
		t.Fatalf("proxy 校验失败应透传")
	}
	// verifyIssuedInputTx：输入行缺失 → ErrInputFence。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("无 issued input 应 ErrInputFence: %v", err)
	}
	// insertOutcome 冲突 → 第二次 matches 不一致 → ErrRequestConflict。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.script("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs",
		[]string{"proxy_id", "input_version", "config_revision", "trigger", "issued_at", "expires_at"},
		[][]driver.Value{{outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.ObservedAt.Add(-time.Second), outcome.ObservedAt.Add(time.Minute)}})
	// 第一次 matches（开头）无行，第二次（冲突后）digest 不同。
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,payload_digest FROM juhe_jobs.proxy_latency_outcomes",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.OwnerFenceToken, outcome.ProxyFenceToken, "w12e-different-digest"}})
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("插入冲突不一致应 ErrRequestConflict: %v", err)
	}
}

// TestW12EAppendOutcomeClaimChain：带 execution claim 的提交链与失败臂。
func TestW12EAppendOutcomeClaimChain(t *testing.T) {
	ctx := context.Background()
	owner := OwnerLease{OwnerID: "w12e-owner", FenceToken: 2}
	proxy := ProxyLease{ProxyID: "w12e-proxy", OwnerID: owner.OwnerID, FenceToken: 4}
	now := time.Now().UTC()
	issued := now.Add(-time.Second)
	expires := now.Add(time.Minute)
	outcome := Outcome{
		OutcomeID: stableOutcomeID("j3a-w12e-claim"), RequestID: "j3a-w12e-claim", ProxyID: proxy.ProxyID,
		ObservedAt: now, InputVersion: 2, ConfigRevision: issued.Format(time.RFC3339Nano),
		Trigger: TriggerPeriodic, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed, Items: []ItemResult{{Status: ItemPassed, HTTPStatus: 200}},
		executionClaimToken: "w12e-claim-token",
	}
	inputsScript := func(rec *wfRecorder) {
		rec.script("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs",
			[]string{"proxy_id", "input_version", "config_revision", "trigger", "issued_at", "expires_at"},
			[][]driver.Value{{outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), issued, expires}})
	}
	// 成功链：claim 校验通过 → 插入 → 删除 claim。
	rec := newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	inputsScript(rec)
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM juhe_jobs.proxy_latency_execution_claims",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_id", "owner_fence_token", "proxy_fence_token", "input_digest", "claim_until"},
		[][]driver.Value{{stableOutcomeID(outcome.RequestID), outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), owner.OwnerID, owner.FenceToken, proxy.FenceToken, "w12e-digest", expires}})
	rec.script("SELECT payload_digest FROM juhe_jobs.proxy_latency_inputs", []string{"payload_digest"}, [][]driver.Value{{"w12e-digest"}})
	rec.script("INSERT INTO juhe_jobs.proxy_latency_outcomes", []string{"outcome_id"}, [][]driver.Value{{outcome.OutcomeID}})
	inserted, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome)
	if !inserted || err != nil {
		t.Fatalf("claim 链应提交成功: inserted=%v err=%v", inserted, err)
	}
	// claim 行字段不匹配 → ErrInputFence。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	inputsScript(rec)
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM juhe_jobs.proxy_latency_execution_claims",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_id", "owner_fence_token", "proxy_fence_token", "input_digest", "claim_until"},
		[][]driver.Value{{stableOutcomeID(outcome.RequestID), outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), "someone-else", owner.FenceToken, proxy.FenceToken, "w12e-digest", expires}})
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("claim 不匹配应 ErrInputFence: %v", err)
	}
	// claim 查询失败。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	inputsScript(rec)
	rec.failQuery("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM juhe_jobs.proxy_latency_execution_claims", errors.New("w12e claim boom"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); err == nil {
		t.Fatalf("claim 查询失败应透传")
	}
	// claim 的 input digest 行不一致 → ErrInputFence。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	inputsScript(rec)
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM juhe_jobs.proxy_latency_execution_claims",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_id", "owner_fence_token", "proxy_fence_token", "input_digest", "claim_until"},
		[][]driver.Value{{stableOutcomeID(outcome.RequestID), outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), owner.OwnerID, owner.FenceToken, proxy.FenceToken, "w12e-digest", expires}})
	rec.script("SELECT payload_digest FROM juhe_jobs.proxy_latency_inputs", []string{"payload_digest"}, [][]driver.Value{{"w12e-other-digest"}})
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("claim digest 不一致应 ErrInputFence: %v", err)
	}
	// delete claim 影响行数 != 1 → ErrInputFence。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	inputsScript(rec)
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM juhe_jobs.proxy_latency_execution_claims",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_id", "owner_fence_token", "proxy_fence_token", "input_digest", "claim_until"},
		[][]driver.Value{{stableOutcomeID(outcome.RequestID), outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), owner.OwnerID, owner.FenceToken, proxy.FenceToken, "w12e-digest", expires}})
	rec.script("SELECT payload_digest FROM juhe_jobs.proxy_latency_inputs", []string{"payload_digest"}, [][]driver.Value{{"w12e-digest"}})
	rec.script("INSERT INTO juhe_jobs.proxy_latency_outcomes", []string{"outcome_id"}, [][]driver.Value{{outcome.OutcomeID}})
	rec.scriptExec("DELETE FROM juhe_jobs.proxy_latency_execution_claims", 0)
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("delete claim 未命中应 ErrInputFence: %v", err)
	}
	// delete claim 执行失败。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	inputsScript(rec)
	rec.script("SELECT outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until FROM juhe_jobs.proxy_latency_execution_claims",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_id", "owner_fence_token", "proxy_fence_token", "input_digest", "claim_until"},
		[][]driver.Value{{stableOutcomeID(outcome.RequestID), outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), owner.OwnerID, owner.FenceToken, proxy.FenceToken, "w12e-digest", expires}})
	rec.script("SELECT payload_digest FROM juhe_jobs.proxy_latency_inputs", []string{"payload_digest"}, [][]driver.Value{{"w12e-digest"}})
	rec.script("INSERT INTO juhe_jobs.proxy_latency_outcomes", []string{"outcome_id"}, [][]driver.Value{{outcome.OutcomeID}})
	rec.failExec("DELETE FROM juhe_jobs.proxy_latency_execution_claims", errors.New("w12e delete boom"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); err == nil {
		t.Fatalf("delete claim 失败应透传")
	}
	// verifyIssuedInputTx 的 issued_at 列非法 → ErrInputFence。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.script("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs",
		[]string{"proxy_id", "input_version", "config_revision", "trigger", "issued_at", "expires_at"},
		[][]driver.Value{{outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), []byte("garbage"), expires}})
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("issued_at 非法应 ErrInputFence: %v", err)
	}
	// verifyIssuedInputTx 身份不匹配 → ErrInputFence。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.script("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs",
		[]string{"proxy_id", "input_version", "config_revision", "trigger", "issued_at", "expires_at"},
		[][]driver.Value{{"another-proxy", outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), issued, expires}})
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("input 身份不匹配应 ErrInputFence: %v", err)
	}
	// verifyIssuedInputTx 查询失败。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.failQuery("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs", errors.New("w12e inputs boom"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); err == nil || strings.Contains(err.Error(), "ErrInputFence") {
		t.Fatalf("inputs 查询失败应包装错误: %v", err)
	}
	// outcome 已过期（observed_at 窗口外）→ ErrInputFence。
	rec = newWFRecorder()
	w12eScriptLeases(rec, owner, proxy)
	rec.script("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs",
		[]string{"proxy_id", "input_version", "config_revision", "trigger", "issued_at", "expires_at"},
		[][]driver.Value{{outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), issued, now.Add(-time.Second)}})
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AppendOutcome(ctx, owner, proxy, outcome); !errors.Is(err, ErrInputFence) {
		t.Fatalf("过期窗口应 ErrInputFence: %v", err)
	}
}

// TestW12EIssueAndLoadArms：IssueInput / LoadCommittedOutcome 的失败臂。
func TestW12EIssueAndLoadArms(t *testing.T) {
	ctx := context.Background()
	// IssueInput：ensureRuntimeSchema 失败（ready=false + begin 失败）。
	rec := newWFRecorder()
	rec.beginErr = errors.New("w12e begin")
	store := &Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}
	if _, err := store.IssueInput(ctx, testInputDraft("p1", TriggerPeriodic)); err == nil {
		t.Fatalf("预检失败应透传")
	}
	// begin 失败（ready=true）。
	rec = newWFRecorder()
	rec.beginErr = errors.New("w12e begin2")
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).IssueInput(ctx, testInputDraft("p1", TriggerPeriodic)); err == nil {
		t.Fatalf("begin 失败应透传")
	}
	// nextInputVersion 失败。
	rec = newWFRecorder()
	rec.failQuery("juhe_jobs.proxy_latency_input_versions", errors.New("w12e version"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).IssueInput(ctx, testInputDraft("p1", TriggerPeriodic)); err == nil {
		t.Fatalf("input version 失败应透传")
	}
	// 持久化 INSERT 失败。
	rec = newWFRecorder()
	rec.script("juhe_jobs.proxy_latency_input_versions", []string{"next_version"}, [][]driver.Value{{int64(1)}})
	rec.failExec("INSERT INTO juhe_jobs.proxy_latency_inputs", errors.New("w12e insert input"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).IssueInput(ctx, testInputDraft("p1", TriggerPeriodic)); err == nil || !strings.Contains(err.Error(), "持久化 J3a issued input 失败") {
		t.Fatalf("INSERT 失败应包装: %v", err)
	}
	// commit 失败。
	rec = newWFRecorder()
	rec.script("juhe_jobs.proxy_latency_input_versions", []string{"next_version"}, [][]driver.Value{{int64(1)}})
	rec.commitErr = errors.New("w12e commit")
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).IssueInput(ctx, testInputDraft("p1", TriggerPeriodic)); err == nil {
		t.Fatalf("commit 失败应透传")
	}

	// LoadCommittedOutcome：真实 SQLite 签发一个输入，再在 PG 录制库上校验。
	sqlite, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "/w12e-issue.sqlite3"})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()
	issued, err := sqlite.IssueInput(ctx, testInputDraft("w12e-proxy", TriggerPeriodic))
	if err != nil {
		t.Fatal(err)
	}
	inputPayload, err := json.Marshal(issued)
	if err != nil {
		t.Fatal(err)
	}
	inputDigest := sha256.Sum256(inputPayload)
	snapshotScript := func(rec *wfRecorder) {
		rec.script("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at,payload_digest FROM juhe_jobs.proxy_latency_inputs",
			[]string{"proxy_id", "input_version", "config_revision", "trigger", "issued_at", "expires_at", "payload_digest"},
			[][]driver.Value{{issued.ProxyID, issued.InputVersion, issued.ConfigRevision, string(issued.Trigger), issued.IssuedAt, issued.ExpiresAt, hex.EncodeToString(inputDigest[:])}})
	}
	// begin 失败。
	rec = newWFRecorder()
	rec.beginErr = errors.New("w12e begin load")
	if _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).LoadCommittedOutcome(ctx, issued); err == nil {
		t.Fatalf("LoadCommittedOutcome begin 失败应透传")
	}
	// snapshot 校验失败（无行）→ ErrInputFence。
	rec = newWFRecorder()
	if _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).LoadCommittedOutcome(ctx, issued); !errors.Is(err, ErrInputFence) {
		t.Fatalf("无 snapshot 应 ErrInputFence: %v", err)
	}
	// snapshot 查询失败（非 NoRows）。
	rec = newWFRecorder()
	rec.failQuery("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at,payload_digest FROM juhe_jobs.proxy_latency_inputs", errors.New("w12e snapshot"))
	if _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).LoadCommittedOutcome(ctx, issued); err == nil {
		t.Fatalf("snapshot 查询失败应透传")
	}
	// 无 committed outcome + commit 失败。
	rec = newWFRecorder()
	snapshotScript(rec)
	rec.commitErr = errors.New("w12e load commit")
	if _, found, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).LoadCommittedOutcome(ctx, issued); found || err == nil {
		t.Fatalf("无 committed + commit 失败: found=%v err=%v", found, err)
	}
	// committed 查询失败。
	rec = newWFRecorder()
	snapshotScript(rec)
	rec.failQuery("FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 AND committed=TRUE", errors.New("w12e committed"))
	if _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).LoadCommittedOutcome(ctx, issued); err == nil {
		t.Fatalf("committed 查询失败应透传")
	}
	// committed 身份不匹配 → ErrRequestConflict。
	outcomePayload := func(mutate func(*Outcome)) []byte {
		outcome := Outcome{
			OutcomeID: "w12e-co", RequestID: issued.RequestID, ProxyID: issued.ProxyID,
			ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion,
			ConfigRevision: issued.ConfigRevision, Trigger: issued.Trigger,
			OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
			Items: []ItemResult{{Status: ItemPassed, HTTPStatus: 200}},
		}
		mutate(&outcome)
		payload, err := json.Marshal(outcome)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	digest := func(payload []byte) string {
		sum := sha256.Sum256(payload)
		return hex.EncodeToString(sum[:])
	}
	rec = newWFRecorder()
	snapshotScript(rec)
	bad := outcomePayload(func(o *Outcome) { o.RequestID = "j3a-other" })
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 AND committed=TRUE", []string{"payload", "payload_digest"}, [][]driver.Value{{bad, digest(bad)}})
	if _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).LoadCommittedOutcome(ctx, issued); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("身份不匹配应 ErrRequestConflict: %v", err)
	}
	// committed 匹配 + commit 失败。
	rec = newWFRecorder()
	snapshotScript(rec)
	good := outcomePayload(func(*Outcome) {})
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 AND committed=TRUE", []string{"payload", "payload_digest"}, [][]driver.Value{{good, digest(good)}})
	rec.commitErr = errors.New("w12e load commit2")
	if _, found, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).LoadCommittedOutcome(ctx, issued); found || err == nil {
		t.Fatalf("匹配 + commit 失败: found=%v err=%v", found, err)
	}
}

// TestW12EListAndFindArms：ListCommittedOutcomes / FindCommittedOutcome。
func TestW12EListAndFindArms(t *testing.T) {
	ctx := context.Background()
	// after 游标 PG 分支 + limit/cursor 参数错误。
	rec := newWFRecorder()
	store := wfNewPGStore(wfOpenRecorderDB(t, rec))
	if _, err := store.ListCommittedOutcomes(ctx, nil, 0); err == nil {
		t.Fatalf("limit 0 应报错")
	}
	if _, err := store.ListCommittedOutcomes(ctx, &OutcomeCursor{}, 10); err == nil {
		t.Fatalf("空游标应报错")
	}
	if _, err := store.ListCommittedOutcomes(ctx, &OutcomeCursor{StoredAt: time.Now(), OutcomeID: "w12e"}, 10); err != nil {
		t.Fatalf("PG after 游标查询应成功: %v", err)
	}
	// 查询失败。
	rec = newWFRecorder()
	rec.failQuery("FROM juhe_jobs.proxy_latency_outcomes WHERE committed=TRUE", errors.New("w12e list"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).ListCommittedOutcomes(ctx, nil, 10); err == nil {
		t.Fatalf("list 查询失败应包装")
	}
	// 行解码失败。
	rec = newWFRecorder()
	rec.script("WHERE committed=TRUE", []string{"outcome_id"}, [][]driver.Value{{"only-one"}})
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).ListCommittedOutcomes(ctx, nil, 10); err == nil {
		t.Fatalf("list scan 失败应包装")
	}
	// 行迭代失败。
	rec = newWFRecorder()
	rec.scriptRowsErr("WHERE committed=TRUE", errors.New("w12e list iter"))
	if _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).ListCommittedOutcomes(ctx, nil, 10); err == nil {
		t.Fatalf("list 迭代失败应包装")
	}
	// FindCommittedOutcome 查询失败。
	rec = newWFRecorder()
	rec.failQuery("WHERE outcome_id=$1 AND committed=TRUE", errors.New("w12e find"))
	if _, found, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).FindCommittedOutcome(ctx, "w12e-o"); found || err == nil {
		t.Fatalf("find 失败应包装: found=%v err=%v", found, err)
	}
	// FindCommittedOutcome 无行 → false, nil。
	rec = newWFRecorder()
	if _, found, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).FindCommittedOutcome(ctx, "w12e-o"); found || err != nil {
		t.Fatalf("find 无行应 false,nil: found=%v err=%v", found, err)
	}
}

// TestW12EVerifyAndAdmitArms：VerifyExecutionInput / AdmitExecution 失败臂。
func TestW12EVerifyAndAdmitArms(t *testing.T) {
	ctx := context.Background()
	owner := OwnerLease{OwnerID: "w12e-owner", FenceToken: 1}
	proxy := ProxyLease{ProxyID: "w12e-proxy", OwnerID: owner.OwnerID, FenceToken: 1}
	// VerifyExecutionInput begin 失败 / owner 失败 / proxy 失败。
	rec := newWFRecorder()
	rec.beginErr = errors.New("w12e begin verify")
	if err := wfNewPGStore(wfOpenRecorderDB(t, rec)).VerifyExecutionInput(ctx, owner, proxy, testIssuedFor("j3a-w12e-verify")); err == nil {
		t.Fatalf("verify begin 失败应透传")
	}
	rec = newWFRecorder()
	rec.failQuery(w12eOwnerRow, errors.New("w12e owner verify"))
	if err := wfNewPGStore(wfOpenRecorderDB(t, rec)).VerifyExecutionInput(ctx, owner, proxy, testIssuedFor("j3a-w12e-verify")); err == nil {
		t.Fatalf("verify owner 失败应透传")
	}
	rec = newWFRecorder()
	rec.failQuery(w12eProxyLeaseRow, errors.New("w12e proxy verify"))
	if err := wfNewPGStore(wfOpenRecorderDB(t, rec)).VerifyExecutionInput(ctx, owner, proxy, testIssuedFor("j3a-w12e-verify")); err == nil {
		t.Fatalf("verify proxy 失败应透传")
	}

	// AdmitExecution：ready=false + begin 失败 → 预检失败。
	rec = newWFRecorder()
	rec.beginErr = errors.New("w12e begin admit preflight")
	if _, _, _, err := (&Store{db: wfOpenRecorderDB(t, rec), mode: StorePostgres}).AdmitExecution(ctx, owner, proxy, testIssuedFor("j3a-w12e-admit")); err == nil {
		t.Fatalf("admit 预检失败应透传")
	}
	// begin 失败。
	rec = newWFRecorder()
	rec.beginErr = errors.New("w12e begin admit")
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, testIssuedFor("j3a-w12e-admit")); err == nil {
		t.Fatalf("admit begin 失败应透传")
	}
	// 输入行不存在 → ErrInputFence。
	rec = newWFRecorder()
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, testIssuedFor("j3a-w12e-admit")); !errors.Is(err, ErrInputFence) {
		t.Fatalf("无输入行应 ErrInputFence: %v", err)
	}
	// 输入行 digest 不一致 → ErrInputFence。
	issued := testIssuedFor("j3a-w12e-admit")
	payload, err := json.Marshal(issued)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	rec = newWFRecorder()
	rec.script("SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1",
		[]string{"payload", "payload_digest"},
		[][]driver.Value{{payload, hex.EncodeToString(sum[:]) + "00"}})
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); !errors.Is(err, ErrInputFence) {
		t.Fatalf("digest 不一致应 ErrInputFence: %v", err)
	}
	// 输入行查询失败。
	rec = newWFRecorder()
	rec.failQuery("SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1", errors.New("w12e admit input"))
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); err == nil {
		t.Fatalf("输入查询失败应透传")
	}
	// 已 committed + commit 失败。
	committed := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: issued.ProxyID,
		ObservedAt: issued.IssuedAt.Add(time.Second), InputVersion: issued.InputVersion,
		ConfigRevision: issued.ConfigRevision, Trigger: issued.Trigger,
		OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed, Items: []ItemResult{{Status: ItemPassed}},
	}
	committedPayload, err := json.Marshal(committed)
	if err != nil {
		t.Fatal(err)
	}
	committedSum := sha256.Sum256(committedPayload)
	inputsOK := func(rec *wfRecorder) {
		rec.script("SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1",
			[]string{"payload", "payload_digest"},
			[][]driver.Value{{payload, hex.EncodeToString(sum[:])}})
	}
	rec = newWFRecorder()
	inputsOK(rec)
	rec.script("SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 AND committed=TRUE FOR UPDATE",
		[]string{"payload", "payload_digest"},
		[][]driver.Value{{committedPayload, hex.EncodeToString(committedSum[:])}})
	rec.commitErr = errors.New("w12e admit replay commit")
	// 已 committed + commit 失败：错误透传（replay 命中的观察结果被 commit
	// 失败吞掉，生产语义即如此）。
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); !strings.Contains(err.Error(), "w12e admit replay commit") {
		t.Fatalf("replay commit 失败应透传: %v", err)
	}
	// 已 committed + commit 成功：返回持久化输入与 replay。
	rec = newWFRecorder()
	inputsOK(rec)
	rec.script("SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 AND committed=TRUE FOR UPDATE",
		[]string{"payload", "payload_digest"},
		[][]driver.Value{{committedPayload, hex.EncodeToString(committedSum[:])}})
	gotPersisted, _, replay, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued)
	if err != nil || replay == nil || gotPersisted.RequestID != issued.RequestID {
		t.Fatalf("replay 应返回持久化输入: replay=%v err=%v", replay, err)
	}
	// verifyOwnerTx 失败。
	rec = newWFRecorder()
	inputsOK(rec)
	rec.failQuery(w12eOwnerRow, errors.New("w12e admit owner"))
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); err == nil {
		t.Fatalf("admit owner 失败应透传")
	}
	// verifyProxyTx 失败。
	rec = newWFRecorder()
	inputsOK(rec)
	rec.failQuery(w12eProxyLeaseRow, errors.New("w12e admit proxy"))
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); err == nil {
		t.Fatalf("admit proxy 失败应透传")
	}
	// 输入已过期 → ErrInputFence。
	expired := testIssuedFor("j3a-w12e-expired")
	expired.IssuedAt = time.Now().Add(-3 * time.Minute)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	expiredPayload, err := json.Marshal(expired)
	if err != nil {
		t.Fatal(err)
	}
	expiredSum := sha256.Sum256(expiredPayload)
	rec = newWFRecorder()
	rec.script("SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1",
		[]string{"payload", "payload_digest"},
		[][]driver.Value{{expiredPayload, hex.EncodeToString(expiredSum[:])}})
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, expired); !errors.Is(err, ErrInputFence) {
		t.Fatalf("过期输入应 ErrInputFence: %v", err)
	}
	// executionClaim 查询失败。
	rec = newWFRecorder()
	inputsOK(rec)
	w12eScriptLeases(rec, owner, proxy)
	rec.failQuery("SELECT claim_until FROM juhe_jobs.proxy_latency_execution_claims", errors.New("w12e claim select"))
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); err == nil {
		t.Fatalf("claim 查询失败应透传")
	}
	// claim 存在且未过期 → ErrRequestInFlight。
	rec = newWFRecorder()
	inputsOK(rec)
	w12eScriptLeases(rec, owner, proxy)
	rec.script("SELECT claim_until FROM juhe_jobs.proxy_latency_execution_claims", []string{"claim_until"}, [][]driver.Value{{time.Now().Add(time.Minute)}})
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); !errors.Is(err, ErrRequestInFlight) {
		t.Fatalf("活跃 claim 应 ErrRequestInFlight: %v", err)
	}
	// claim INSERT 失败。
	rec = newWFRecorder()
	inputsOK(rec)
	w12eScriptLeases(rec, owner, proxy)
	rec.failExec("INSERT INTO juhe_jobs.proxy_latency_execution_claims", errors.New("w12e claim insert"))
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); err == nil || !strings.Contains(err.Error(), "创建 J3a execution claim 失败") {
		t.Fatalf("claim insert 失败应包装: %v", err)
	}
	// commit 失败。
	rec = newWFRecorder()
	inputsOK(rec)
	w12eScriptLeases(rec, owner, proxy)
	rec.commitErr = errors.New("w12e admit commit")
	if _, _, _, err := wfNewPGStore(wfOpenRecorderDB(t, rec)).AdmitExecution(ctx, owner, proxy, issued); err == nil {
		t.Fatalf("admit commit 失败应透传")
	}
}

// TestW12EBeginTxAndSQLiteModeArms：beginTx SET LOCAL 失败 + SQLite 模式臂。
func TestW12EBeginTxAndSQLiteModeArms(t *testing.T) {
	ctx := context.Background()
	// beginTx 的 SET LOCAL 失败（VerifyOwnerLease 走 beginTx）。
	rec := newWFRecorder()
	rec.failExec("SET LOCAL statement_timeout", errors.New("w12e setlocal"))
	store := wfNewPGStore(wfOpenRecorderDB(t, rec))
	if err := store.VerifyOwnerLease(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}); err == nil || !strings.Contains(err.Error(), "transaction timeout 失败") {
		t.Fatalf("SET LOCAL 失败应包装: %v", err)
	}
	// verifyOwnerTx / verifyProxyTx 的 SQLite 字符串分支：释放未命中 → lost。
	rec = newWFRecorder()
	rec.scriptExec("UPDATE proxy_latency_owner_leases", 0)
	sqliteStore := &Store{db: wfOpenRecorderDB(t, rec), mode: StoreSQLite}
	if err := sqliteStore.RenewOwnerLease(ctx, OwnerLease{OwnerID: "w12e-o", FenceToken: 1}, time.Minute); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("SQLite lease 未命中应 lost: %v", err)
	}
	// AppendOutcome SQLite 模式 insert 失败：mode 伪装为 SQLite 的录制库。
	rec = newWFRecorder()
	rec.failExec("INSERT INTO proxy_latency_outcomes", errors.New("w12e sqlite insert"))
	fakeSQLite := &Store{db: wfOpenRecorderDB(t, rec), mode: StoreSQLite}
	outcome := Outcome{
		OutcomeID: "w12e-o", RequestID: "j3a-w12e-sqlite", ProxyID: "w12e-p",
		ObservedAt: time.Now().UTC(), InputVersion: 1,
		ConfigRevision: time.Now().UTC().Format(time.RFC3339Nano), Trigger: TriggerPeriodic,
		OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
		Items: []ItemResult{{Status: ItemPassed}},
	}
	if _, err := fakeSQLite.AppendOutcome(ctx, OwnerLease{OwnerID: "o", FenceToken: 1}, ProxyLease{ProxyID: "w12e-p", OwnerID: "o", FenceToken: 1}, outcome); err == nil {
		t.Fatalf("SQLite insert 失败应透传")
	}
}

func testIssuedFor(requestID string) IssuedInput {
	now := time.Now().UTC()
	return IssuedInput{
		RequestID: requestID, ProxyID: "w12e-proxy", InputVersion: 1,
		ConfigRevision: now.Add(-time.Second).Format(time.RFC3339Nano), Trigger: TriggerPeriodic,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.invalid/v1"}},
	}
}
