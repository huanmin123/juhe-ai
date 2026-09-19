// 波次 w16j 共享 fixture：w1cover 覆盖库重建后，jobs 侧自有的 juhe_jobs
// schema 与 J3a proxy_latency_* 预 provision 表会随重建消失。maintenance
// 权威 DDL 按契约不包含它们：accounthealth/proxylatency 的 EnsureSchema/
// CheckSchema 只校验不创建（"缺少外部 bootstrap 创建的 juhe_jobs schema"），
// schema 预置属外部 bootstrap 责任，此前靠人工补表、每次重建即复发。
// w16jEnsurePGJobsFixture 把预置收敛为幂等自愈入口：无论覆盖库新建、重建
// 还是被并行会话清理，PG 门禁测试调用后 J1 EnsureSchema 与 J3a CheckSchema
// 的前置即满足。只做加法幂等 DDL，不写业务行；连接串不落日志。
package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// w16jJ3aPresetSchemaStatements 是 J3a jobs store 的预 provision DDL
// （maintenance internal/j3aproxylatency postgresSchema 同源；J3a 契约是
// 只读 CheckSchema，jobs 不做 DDL，覆盖库需先预置）。加法幂等，建表不回滚
// （w14j/w16i seed 同类先例）。
var w16jJ3aPresetSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_owner_leases (
		lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_proxy_leases (
		proxy_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_outcomes (
		outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_input_versions (
		proxy_id TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_inputs (
		request_id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, UNIQUE(proxy_id, input_version))`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_execution_claims (
		request_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_id TEXT NOT NULL, owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL, input_digest TEXT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_proxy ON juhe_jobs.proxy_latency_outcomes(proxy_id, observed_at)`,
	`CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_cursor ON juhe_jobs.proxy_latency_outcomes(stored_at, outcome_id)`,
}

// w16jEnsurePGJobsFixture 幂等预置覆盖库上 jobs 侧自有的 PG fixture：外部
// bootstrap 契约要求的 juhe_jobs schema（覆盖库重建后缺失，J1 EnsureSchema
// 的硬前置）+ J3a proxy_latency_* 预 provision 表（J3a CheckSchema 的只读
// 契约面）。语句失败按既有约定 t.Skip：可达但无法预置说明共享库越权或形状
// 异常，由门禁的其余用例暴露，不在此处静默吞掉。
func w16jEnsurePGJobsFixture(t *testing.T, pgURL string) {
	t.Helper()
	if pgURL == "" {
		t.Skip("w16j: 覆盖库连接串不可用")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w16j: 打开覆盖库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("w16j: 覆盖库不可达: %v", err)
	}
	for _, statement := range append([]string{`CREATE SCHEMA IF NOT EXISTS juhe_jobs`}, w16jJ3aPresetSchemaStatements...) {
		if _, err := db.Exec(statement); err != nil {
			t.Skipf("w16j: juhe_jobs fixture 预置失败: %v", err)
		}
	}
}

// w16jContractlessInputURL 返回同一 PG 服务器上无业务契约面的输入库连接串
// （标准 postgres 维护库）。J1/J3a 的 CheckContract 用 schema 全限定关系名
// 做零行只读校验，search_path 指向空 schema 不会使其失败（w16j 实证：契约
// 误通过后 run() 挂进 supervisor 循环直至测试超时）；权威形状重建后共享库
// 的契约面必然完整，故改在 postgres 库上注入"可达但缺契约"失败臂：契约
// 预检在首个全限定关系上失败，装配收敛为退出码 1。只读零写、不创建任何
// 共享对象；postgres 库意外包含业务契约时 t.Skip，防止契约误过挂起 run()。
func w16jContractlessInputURL(t *testing.T, pgURL string) string {
	t.Helper()
	base := pgURL
	if index := strings.Index(base, "?"); index >= 0 {
		base = base[:index]
	}
	index := strings.LastIndex(base, "/")
	if index < 0 {
		t.Skip("w16j: 覆盖库连接串无法解析库名")
	}
	inputURL := base[:index+1] + "postgres"
	db, err := sql.Open("pgx", inputURL)
	if err != nil {
		t.Skipf("w16j: 打开契约缺失输入库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("w16j: 契约缺失输入库不可达: %v", err)
	}
	var one int
	if err := db.QueryRow(`SELECT 1 FROM juhe_business.accounts LIMIT 0`).Scan(&one); err == nil {
		t.Skip("w16j: postgres 维护库意外包含业务契约面，无法注入契约失败臂")
	}
	return inputURL
}
