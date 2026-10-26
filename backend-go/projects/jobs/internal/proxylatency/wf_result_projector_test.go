package proxylatency

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// 本文件覆盖 J3a Go ResultProjector 的业务投影契约（SQLite 真库语义）：
// NewResultProjector 校验、CheckContract、Drain 游标推进、receipt 幂等、
// proxy_profiles CAS 围栏、manual no-targets/outbound 投影与 Ready 记账。
// jobs Store 用真实 SQLite 实例提交 committed outcome；业务库用独立内存
// SQLite 建三张投影表，PG 分支的 SQL 文本契约另用录制驱动断言。
//
// 组B保留（SQLite-only 生产臂，PLAN-20261024T051925945Z proxylatency 切片
// 裁决，整文件不迁，随生产 sqlite 臂删除批次退役）。证据：
//  1. projector.mode 继承 store.mode——迁 PG 必须 同时 将业务库换成 juhe_business
//     真实 PG schema（PG CheckContract 需要 accounts 与 account_list_availability_*
//     共 8 张表及触发器契约，DDL 复刻面大且无生产建表路径可复用）；
//  2. proxy_profiles.updated_at 在 PG 为 timestamptz，CAS 围栏经
//     `updated_at=$n::timestamptz` 强类型比较——本文件的"CAS 未命中（同刻不同
//     文本，wfProjRevisionGranular）"变体是 SQLite TEXT 列特有语义，PG 下
//     同刻不同文本被 cast 归一，断言无法逐字节保持；
//  3. PG 臂时间形态（to_char 微秒文本围栏读出）与本文件 RFC3339Nano 文本
//     断言不兼容，重钉面覆盖全部投影读断言。
// 纯 Store 面（wf_runner_units/wf_outbound_success/w5 的 jobs-store 消费）
// 已迁真实 PG（proxylatency_pgkit_test.go）。

// wfProjBase 是测试固定的当前时间基准（贴近真实时钟，仅相对偏移参与断言；
// jobs Store 的输入围栏按真实墙钟校验 input 有效期，不能用任意固定日期）。
var wfProjBase = time.Now().UTC().Truncate(time.Second)

// wfProjRevision 是基准前一小时派生的规范 config revision（RFC3339 UTC）。
var wfProjRevision = wfProjBase.Add(-time.Hour).UTC().Format(time.RFC3339Nano)

// wfProjRevisionGranular 与 wfProjRevision 同一时刻但文本不同（CAS 文本围栏）。
var wfProjRevisionGranular = wfProjBase.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000000Z")

func wfNewProjector(t *testing.T, store *Store, business *sql.DB) *ResultProjector {
	t.Helper()
	projector, err := NewResultProjector(store, business, ResultProjectorConfig{
		ConsumerKey: "wf-consumer", PollInterval: time.Minute, BatchSize: 10,
		Now: func() time.Time { return wfProjBase },
	}, nil)
	if err != nil {
		t.Fatalf("构造 projector 失败: %v", err)
	}
	return projector
}

// wfSeedProxyRow 直接写业务库 proxy_profiles 围栏行。
func wfSeedProxyRow(t *testing.T, db *sql.DB, id, revision string, lastTestedAt any) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO proxy_profiles(id, updated_at, last_tested_at, test_status) VALUES(?,?,?, '')`, id, revision, lastTestedAt); err != nil {
		t.Fatalf("播种 proxy 行失败: %v", err)
	}
}

// wfCommitter 在同一 owner lease 下提交多条 committed outcome（固定时钟）。
type wfCommitter struct {
	t     *testing.T
	store *Store
	owner OwnerLease
	// last 保存最近一次 IssueInput 结果，供后续断言复用。
	last IssuedInput
}

func newWFCommitter(t *testing.T, store *Store) *wfCommitter {
	t.Helper()
	owner, ok, err := store.AcquireOwnerLease(context.Background(), "wf-owner", time.Hour)
	if err != nil || !ok {
		t.Fatalf("获取 owner lease 失败 ok=%v err=%v", ok, err)
	}
	return &wfCommitter{t: t, store: store, owner: owner}
}

// commitFunc 走完整 Store 管线提交 committed outcome，mutate 允许定制字段。
func (c *wfCommitter) commitFunc(proxyID, revision string, mutate func(*Outcome)) Outcome {
	c.t.Helper()
	ctx := context.Background()
	proxy, ok, err := c.store.AcquireProxyLease(ctx, c.owner, proxyID, time.Hour)
	if err != nil || !ok {
		c.t.Fatalf("获取 proxy lease 失败 ok=%v err=%v", ok, err)
	}
	draft := InputDraft{
		ProxyID: proxyID, ConfigRevision: revision, Trigger: TriggerPeriodic,
		IssuedAt: wfProjBase, ExpiresAt: wfProjBase.Add(5 * time.Minute),
		PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType:     "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
	issued, err := c.store.IssueInput(ctx, draft)
	if err != nil {
		c.t.Fatalf("IssueInput 失败: %v", err)
	}
	c.last = issued
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID,
		ProxyID: issued.ProxyID, ObservedAt: wfProjBase.Add(time.Second),
		InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: c.owner.FenceToken, ProxyFenceToken: proxy.FenceToken,
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
	if mutate != nil {
		mutate(&outcome)
	}
	if _, err := c.store.AppendOutcome(ctx, c.owner, proxy, outcome); err != nil {
		c.t.Fatalf("AppendOutcome 失败: %v", err)
	}
	// 提交后释放 proxy lease，允许测试方再次领取同一代理做 admission 语义验证。
	if err := c.store.ReleaseProxyLease(ctx, proxy); err != nil {
		c.t.Fatalf("释放 proxy lease 失败: %v", err)
	}
	return outcome
}

func (c *wfCommitter) commit(proxyID, revision string) Outcome {
	return c.commitFunc(proxyID, revision, nil)
}

// wfBusinessProxyState 读取业务库投影结果。
func wfBusinessProxyState(t *testing.T, db *sql.DB, proxyID string) (status string, latency sql.NullInt64, message sql.NullString, testedAt sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT test_status, latency_ms, last_test_message, last_tested_at FROM proxy_profiles WHERE id=?`, proxyID).
		Scan(&status, &latency, &message, &testedAt); err != nil {
		t.Fatalf("读取业务投影失败: %v", err)
	}
	return status, latency, message, testedAt
}

func TestWFResultProjectorCheckContractNilGuards(t *testing.T) {
	var nilProjector *ResultProjector
	if err := nilProjector.CheckContract(context.Background()); err == nil {
		t.Fatal("nil projector 必须报未初始化")
	}
	if err := (&ResultProjector{}).CheckContract(context.Background()); err == nil {
		t.Fatal("缺业务库必须报未初始化")
	}
}

