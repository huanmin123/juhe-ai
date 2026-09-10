package proxylatency

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 本文件用录制驱动覆盖 Store 的 PostgreSQL 专属分支：运行时 schema 契约
// 检查（表/列/索引/约束逐项校验与各失败模式）、EnsureSchema 语句装配、
// owner/proxy lease 的 UPSERT/续租/释放、IssueInput/AppendOutcome/
// AdmitExecution 的 PG 语句与 RETURNING 语义。夹具直接从 contracts 包的
// 契约定义生成，避免手写漂移。

// wfNewPGStore 构造跳过运行时 schema 检查的 PG 模式 Store（仅测试用）。
func wfNewPGStore(db *sql.DB) *Store {
	return &Store{db: db, mode: StorePostgres, postgresSchemaReady: true}
}

// wfScriptSchema 登记一次完整 schema 契约检查所需的全部查询结果。
func wfScriptSchema(t *testing.T, rec *wfRecorder) {
	t.Helper()
	// pg_namespace：schema owner = 当前角色。
	rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"wf_jobs_role", "wf_jobs_role"}})
	// information_schema.tables：契约要求的全部表名。
	tableRows := make([][]driver.Value, 0, len(contracts.J3AProxyLatencyTables))
	for _, table := range contracts.J3AProxyLatencyTables {
		tableRows = append(tableRows, []driver.Value{table})
	}
	rec.script("FROM information_schema.tables WHERE table_schema='juhe_jobs'", []string{"table_name"}, tableRows)
	// information_schema.columns：契约要求的全部列定义。
	var columnRows [][]driver.Value
	for _, table := range contracts.J3AProxyLatencyTables {
		columns := contracts.J3AProxyLatencyColumns[table]
		for column, spec := range columns {
			nullable := "NO"
			if spec.Nullable {
				nullable = "YES"
			}
			columnRows = append(columnRows, []driver.Value{table, column, spec.DataType, spec.UdtName, nullable})
		}
	}
	rec.script("FROM information_schema.columns WHERE table_schema='juhe_jobs'", []string{"table_name", "column_name", "data_type", "udt_name", "is_nullable"}, columnRows)
	// pg_constraint：契约要求的 p/u 约束定义。
	var constraintRows [][]driver.Value
	for _, table := range contracts.J3AProxyLatencyTables {
		for _, expected := range contracts.J3AProxyLatencyConstraints[table] {
			constraintRows = append(constraintRows, []driver.Value{table, "PRIMARY KEY " + strings.ToUpper(strings.TrimPrefix(expected, "primary key "))})
		}
	}
	// 直接使用契约原文（pg_get_constraintdef 输出为小写）。
	constraintRows = nil
	for _, table := range contracts.J3AProxyLatencyTables {
		for _, expected := range contracts.J3AProxyLatencyConstraints[table] {
			constraintRows = append(constraintRows, []driver.Value{table, expected})
		}
	}
	rec.script("FROM pg_constraint AS c", []string{"relname", "def"}, constraintRows)
	// pg_indexes：契约要求的索引定义。
	var indexRows [][]driver.Value
	for name, expected := range contracts.J3AProxyLatencyIndexes {
		indexRows = append(indexRows, []driver.Value{name, "CREATE INDEX " + name + " " + expected})
	}
	rec.script("FROM pg_indexes WHERE schemaname='juhe_jobs'", []string{"indexname", "indexdef"}, indexRows)
}

func TestWFStorePGSchemaContractCheck(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	store := &Store{db: db, mode: StorePostgres, postgresSchemaReady: false}

	// 完整契约 → 通过。
	wfScriptSchema(t, rec)
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatalf("完整契约检查失败: %v", err)
	}

	// 缺表 → fail closed。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	store2 := &Store{db: db2, mode: StorePostgres, postgresSchemaReady: false}
	rec2.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"role", "role"}})
	rec2.script("FROM information_schema.tables WHERE table_schema='juhe_jobs'", []string{"table_name"}, [][]driver.Value{{"proxy_latency_owner_leases"}})
	err := store2.CheckSchema(context.Background())
	if err == nil || !strings.Contains(err.Error(), "缺少预置表") {
		t.Fatalf("缺表 err=%v", err)
	}

	// 列类型不兼容。
	rec3 := newWFRecorder()
	db3 := wfOpenRecorderDB(t, rec3)
	store3 := &Store{db: db3, mode: StorePostgres, postgresSchemaReady: false}
	rec3.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"role", "role"}})
	tableRows := make([][]driver.Value, 0, len(contracts.J3AProxyLatencyTables))
	for _, table := range contracts.J3AProxyLatencyTables {
		tableRows = append(tableRows, []driver.Value{table})
	}
	rec3.script("FROM information_schema.tables WHERE table_schema='juhe_jobs'", []string{"table_name"}, tableRows)
	var columnRows [][]driver.Value
	for _, table := range contracts.J3AProxyLatencyTables {
		for column, spec := range contracts.J3AProxyLatencyColumns[table] {
			if table == "proxy_latency_owner_leases" && column == "fence_token" {
				spec.DataType = "text"
				spec.UdtName = "text"
			}
			nullable := "NO"
			if spec.Nullable {
				nullable = "YES"
			}
			columnRows = append(columnRows, []driver.Value{table, column, spec.DataType, spec.UdtName, nullable})
		}
	}
	rec3.script("FROM information_schema.columns WHERE table_schema='juhe_jobs'", []string{"t", "c", "d", "u", "n"}, columnRows)
	if err := store3.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "列定义不兼容") {
		t.Fatalf("列不兼容 err=%v", err)
	}

	// 缺索引：完整契约之后追加空索引结果（FIFO 消费，第二次检查读空）。
	rec4 := newWFRecorder()
	db4 := wfOpenRecorderDB(t, rec4)
	store4 := &Store{db: db4, mode: StorePostgres, postgresSchemaReady: false}
	rec4.script("FROM pg_indexes WHERE schemaname='juhe_jobs'", []string{"indexname", "indexdef"}, nil)
	wfScriptSchema(t, rec4)
	if err := store4.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "缺少预置索引") {
		t.Fatalf("缺索引 err=%v", err)
	}

	// 索引定义漂移：第二次检查返回错误定义。
	rec4b := newWFRecorder()
	db4b := wfOpenRecorderDB(t, rec4b)
	store4b := &Store{db: db4b, mode: StorePostgres, postgresSchemaReady: false}
	var drifted [][]driver.Value
	for name := range contracts.J3AProxyLatencyIndexes {
		drifted = append(drifted, []driver.Value{name, "CREATE INDEX " + name + " ON juhe_jobs.x USING btree (wrong)"})
	}
	rec4b.script("FROM pg_indexes WHERE schemaname='juhe_jobs'", []string{"indexname", "indexdef"}, drifted)
	wfScriptSchema(t, rec4b)
	if err := store4b.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "索引定义不兼容") {
		t.Fatalf("索引漂移 err=%v", err)
	}

	// schema owner 与当前角色不一致。
	rec5 := newWFRecorder()
	db5 := wfOpenRecorderDB(t, rec5)
	store5 := &Store{db: db5, mode: StorePostgres, postgresSchemaReady: false}
	rec5.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"other_role", "wf_jobs_role"}})
	if err := store5.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "owner 必须是当前 jobs role") {
		t.Fatalf("owner 不一致 err=%v", err)
	}

	// schema 缺失。
	rec6 := newWFRecorder()
	db6 := wfOpenRecorderDB(t, rec6)
	store6 := &Store{db: db6, mode: StorePostgres, postgresSchemaReady: false}
	if err := store6.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "缺少外部 bootstrap") {
		t.Fatalf("缺 schema err=%v", err)
	}
}

func TestWFStorePGEnsureSchemaAndLeases(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	store := &Store{db: db, mode: StorePostgres}
	ctx := context.Background()

	// EnsureSchema：owner 校验 + 全部 DDL 语句装配。
	rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"wf_jobs_role", "wf_jobs_role"}})
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema 失败: %v", err)
	}
	joined := ""
	for _, statement := range rec.all() {
		joined += statement.query + "\n"
	}
	for _, required := range []string{"CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_owner_leases", "CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_outcomes"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("EnsureSchema 缺少 %q", required)
		}
	}

	// PG lease 生命周期：使用独立实例（postgresSchemaReady 已置位）。
	recLease := newWFRecorder()
	dbLease := wfOpenRecorderDB(t, recLease)
	pgStore := wfNewPGStore(dbLease)
	recLease.script("INSERT INTO juhe_jobs.proxy_latency_owner_leases", []string{"fence_token"}, [][]driver.Value{{int64(1)}})
	owner, ok, err := pgStore.AcquireOwnerLease(ctx, "wf-pg-owner", time.Hour)
	if err != nil || !ok || owner.FenceToken != 1 {
		t.Fatalf("AcquireOwnerLease owner=%+v ok=%v err=%v", owner, ok, err)
	}
	// 已有租约且未过期 → no row → not acquired。
	if _, ok, err := pgStore.AcquireOwnerLease(ctx, "wf-other", time.Hour); err != nil || ok {
		t.Fatalf("未过期租约必须拒绝 ok=%v err=%v", ok, err)
	}
	// RenewOwnerLease：命中 1 行成功，0 行 → ErrOwnerLeaseLost。
	if err := pgStore.RenewOwnerLease(ctx, owner, time.Hour); err != nil {
		t.Fatalf("RenewOwnerLease 失败: %v", err)
	}
	recLease.scriptExec("UPDATE juhe_jobs.proxy_latency_owner_leases SET lease_until=statement_timestamp()+", 0)
	if err := pgStore.RenewOwnerLease(ctx, owner, time.Hour); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("续租丢失 err=%v", err)
	}
	// ReleaseOwnerLease。
	if err := pgStore.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatalf("ReleaseOwnerLease 失败: %v", err)
	}
	recLease.scriptExec("UPDATE juhe_jobs.proxy_latency_owner_leases SET lease_until=statement_timestamp(),", 0)
	if err := pgStore.ReleaseOwnerLease(ctx, owner); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("释放丢失 err=%v", err)
	}

	// VerifyOwnerLease：租约行有效。
	recLease.script("FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' FOR UPDATE",
		[]string{"owner_id", "fence_token", "lease_until"},
		[][]driver.Value{{owner.OwnerID, owner.FenceToken, time.Now().Add(time.Hour)}})
	if err := pgStore.VerifyOwnerLease(ctx, owner); err != nil {
		t.Fatalf("VerifyOwnerLease 失败: %v", err)
	}

	// AcquireProxyLease：verify owner + UPSERT RETURNING。
	recLease.script("FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' FOR UPDATE",
		[]string{"owner_id", "fence_token", "lease_until"},
		[][]driver.Value{{owner.OwnerID, owner.FenceToken, time.Now().Add(time.Hour)}})
	recLease.script("INSERT INTO juhe_jobs.proxy_latency_proxy_leases", []string{"fence_token"}, [][]driver.Value{{int64(1)}})
	proxyLease, acquired, err := pgStore.AcquireProxyLease(ctx, owner, "p-pg", time.Hour)
	if err != nil || !acquired || proxyLease.FenceToken != 1 {
		t.Fatalf("AcquireProxyLease proxy=%+v acquired=%v err=%v", proxyLease, acquired, err)
	}
	if err := pgStore.ReleaseProxyLease(ctx, proxyLease); err != nil {
		t.Fatalf("ReleaseProxyLease 失败: %v", err)
	}
	recLease.scriptExec("UPDATE juhe_jobs.proxy_latency_proxy_leases SET lease_until=statement_timestamp()", 0)
	if err := pgStore.ReleaseProxyLease(ctx, proxyLease); !errors.Is(err, ErrProxyLeaseLost) {
		t.Fatalf("proxy 释放丢失 err=%v", err)
	}
}

func TestWFStorePGAppendOutcomeAndAdmission(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	pgStore := wfNewPGStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	rec.script("INSERT INTO juhe_jobs.proxy_latency_owner_leases", []string{"fence_token"}, [][]driver.Value{{int64(2)}})
	owner, ok, err := pgStore.AcquireOwnerLease(ctx, "wf-pg-owner", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	rec.script("FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' FOR UPDATE",
		[]string{"owner_id", "fence_token", "lease_until"},
		[][]driver.Value{{owner.OwnerID, owner.FenceToken, now.Add(time.Hour)}})
	rec.script("INSERT INTO juhe_jobs.proxy_latency_proxy_leases", []string{"fence_token"}, [][]driver.Value{{int64(2)}})
	proxy, acquired, err := pgStore.AcquireProxyLease(ctx, owner, "p-pg", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("proxy lease acquired=%v err=%v", acquired, err)
	}

	rec.script("INSERT INTO juhe_jobs.proxy_latency_input_versions", []string{"next_version-1"}, [][]driver.Value{{int64(3)}})
	draft := InputDraft{
		ProxyID: "p-pg", ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "10.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
	issued, err := pgStore.IssueInput(ctx, draft)
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}

	payload, err := json.Marshal(issued)
	if err != nil {
		t.Fatalf("编码 issued 失败: %v", err)
	}
	digest, err := canonicalJSONDigest(issued)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	rec.script("SELECT payload,payload_digest FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1 FOR UPDATE",
		[]string{"payload", "digest"}, [][]driver.Value{{payload, digest}})
	rec.script("SELECT owner_id,fence_token,lease_until FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' FOR UPDATE",
		[]string{"owner_id", "fence_token", "lease_until"},
		[][]driver.Value{{owner.OwnerID, owner.FenceToken, now.Add(time.Hour)}})
	rec.script("SELECT owner_id,fence_token,lease_until FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1 FOR UPDATE",
		[]string{"owner_id", "fence_token", "lease_until"},
		[][]driver.Value{{owner.OwnerID, proxy.FenceToken, now.Add(time.Hour)}})
	resolved, claimToken, replay, err := pgStore.AdmitExecution(ctx, owner, proxy, issued)
	if err != nil || replay != nil || claimToken == "" || resolved.RequestID != issued.RequestID {
		t.Fatalf("AdmitExecution claim=%q replay=%+v err=%v", claimToken, replay, err)
	}
	if err := pgStore.ReleaseExecutionClaim(ctx, issued.RequestID, claimToken); err != nil {
		t.Fatalf("ReleaseExecutionClaim 失败: %v", err)
	}

	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: "p-pg",
		ObservedAt: now.Add(time.Second), InputVersion: issued.InputVersion, ConfigRevision: wfProjRevision,
		Trigger: TriggerPeriodic, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
	rec.script("SELECT owner_id,fence_token,lease_until FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' FOR UPDATE",
		[]string{"owner_id", "fence_token", "lease_until"},
		[][]driver.Value{{owner.OwnerID, owner.FenceToken, now.Add(time.Hour)}})
	rec.script("SELECT owner_id,fence_token,lease_until FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1 FOR UPDATE",
		[]string{"owner_id", "fence_token", "lease_until"},
		[][]driver.Value{{owner.OwnerID, proxy.FenceToken, now.Add(time.Hour)}})
	rec.script("SELECT proxy_id,input_version,config_revision,trigger,issued_at,expires_at FROM juhe_jobs.proxy_latency_inputs WHERE request_id=$1 FOR UPDATE",
		[]string{"proxy_id", "input_version", "config_revision", "trigger", "issued_at", "expires_at"},
		[][]driver.Value{{"p-pg", issued.InputVersion, wfProjRevision, string(TriggerPeriodic), issued.IssuedAt, issued.ExpiresAt}})
	rec.script("INSERT INTO juhe_jobs.proxy_latency_outcomes", []string{"outcome_id"}, [][]driver.Value{{outcome.OutcomeID}})
	committed, err := pgStore.AppendOutcome(ctx, owner, proxy, outcome)
	if err != nil || !committed {
		t.Fatalf("AppendOutcome committed=%v err=%v", committed, err)
	}
	joined := ""
	for _, statement := range rec.all() {
		joined += statement.query + "\n"
	}
	for _, required := range []string{
		"INSERT INTO juhe_jobs.proxy_latency_outcomes(outcome_id,request_id,proxy_id,input_version,config_revision,trigger,owner_fence_token,proxy_fence_token,observed_at,stored_at,payload,payload_digest,committed)",
		"INSERT INTO juhe_jobs.proxy_latency_execution_claims(request_id,claim_token,outcome_id,proxy_id,input_version,config_revision,trigger,owner_id,owner_fence_token,proxy_fence_token,input_digest,claim_until,updated_at)",
		"DELETE FROM juhe_jobs.proxy_latency_execution_claims WHERE request_id=$1 AND claim_token=$2",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("PG AppendOutcome/Admission 缺少 %q", required)
		}
	}
}
