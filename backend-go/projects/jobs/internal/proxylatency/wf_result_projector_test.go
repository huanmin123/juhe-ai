package proxylatency

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 J3a Go ResultProjector 的业务投影契约（SQLite 真库语义）：
// NewResultProjector 校验、CheckContract、Drain 游标推进、receipt 幂等、
// proxy_profiles CAS 围栏、manual no-targets/outbound 投影与 Ready 记账。
// jobs Store 用真实 SQLite 实例提交 committed outcome；业务库用独立内存
// SQLite 建三张投影表，PG 分支的 SQL 文本契约另用录制驱动断言。

// wfProjBase 是测试固定的当前时间基准（贴近真实时钟，仅相对偏移参与断言；
// jobs Store 的输入围栏按真实墙钟校验 input 有效期，不能用任意固定日期）。
var wfProjBase = time.Now().UTC().Truncate(time.Second)

// wfProjRevision 是基准前一小时派生的规范 config revision（RFC3339 UTC）。
var wfProjRevision = wfProjBase.Add(-time.Hour).UTC().Format(time.RFC3339Nano)

// wfProjRevisionGranular 与 wfProjRevision 同一时刻但文本不同（CAS 文本围栏）。
var wfProjRevisionGranular = wfProjBase.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000000Z")

// wfOpenBusinessDB 打开独立 SQLite 业务库并创建三张投影表。
func wfOpenBusinessDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf-business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("打开业务库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭业务库失败: %v", err)
		}
	})
	for _, ddl := range []string{
		`CREATE TABLE proxy_profiles (
			id TEXT PRIMARY KEY, updated_at TEXT NOT NULL, last_tested_at TEXT,
			test_status TEXT NOT NULL DEFAULT '', latency_ms INTEGER,
			last_test_message TEXT, outbound_ip TEXT, outbound_region TEXT)`,
		`CREATE TABLE proxy_latency_projection_receipts (
			outcome_id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL, input_version INTEGER NOT NULL,
			disposition TEXT NOT NULL, reason TEXT, applied_at TEXT NOT NULL)`,
		`CREATE TABLE proxy_latency_projection_cursors (
			consumer_key TEXT PRIMARY KEY, stored_at TEXT, outcome_id TEXT, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("初始化业务表失败: %v", err)
		}
	}
	return db
}

// wfOpenJobsStore 打开独立 SQLite jobs Store。
func wfOpenJobsStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "wf-jobs.sqlite3")})
	if err != nil {
		t.Fatalf("打开 jobs Store 失败: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭 jobs Store 失败: %v", err)
		}
	})
	return store
}

// wfNewProjector 以固定时钟构造 projector。
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
	// last 保存最近一次 IssueInput 结果，供 LoadCommittedOutcome 等读取方复用。
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

func TestWFNewResultProjectorValidation(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	valid := ResultProjectorConfig{ConsumerKey: "wf", PollInterval: time.Minute, BatchSize: 10}

	if _, err := NewResultProjector(nil, business, valid, nil); err == nil {
		t.Fatal("缺 Store 必须拒绝")
	}
	if _, err := NewResultProjector(store, nil, valid, nil); err == nil {
		t.Fatal("缺业务库必须拒绝")
	}
	if _, err := NewResultProjector(store, business, valid, nil); err != nil {
		t.Fatalf("完整配置必须可用: %v", err)
	}
	invalidMode := &Store{db: business, mode: StoreMode("bogus")}
	if _, err := NewResultProjector(invalidMode, business, valid, nil); err == nil {
		t.Fatal("非法 Store mode 必须拒绝")
	}
	longKey := strings.Repeat("k", 201)
	if _, err := NewResultProjector(store, business, ResultProjectorConfig{ConsumerKey: longKey, PollInterval: time.Minute}, nil); err == nil {
		t.Fatal("超长 consumer key 必须拒绝")
	}
	for _, interval := range []time.Duration{50 * time.Millisecond, 2 * time.Minute} {
		if _, err := NewResultProjector(store, business, ResultProjectorConfig{ConsumerKey: "wf", PollInterval: interval}, nil); err == nil {
			t.Fatalf("poll interval %v 必须拒绝", interval)
		}
	}
	if _, err := NewResultProjector(store, business, ResultProjectorConfig{ConsumerKey: "wf", PollInterval: time.Minute, BatchSize: -1}, nil); err == nil {
		t.Fatal("负 batch size 必须拒绝")
	}
	if _, err := NewResultProjector(store, business, ResultProjectorConfig{ConsumerKey: "wf", PollInterval: time.Minute, BatchSize: maxProxyLatencyWorkItems + 1}, nil); err == nil {
		t.Fatal("超大 batch size 必须拒绝")
	}
	// 默认值回填：空 consumer key、poll interval 与 batch size。
	filled, err := NewResultProjector(store, business, ResultProjectorConfig{}, nil)
	if err != nil {
		t.Fatalf("默认配置必须可用: %v", err)
	}
	if filled.cfg.ConsumerKey != defaultResultProjectionConsumer {
		t.Fatalf("默认 consumer=%q", filled.cfg.ConsumerKey)
	}
	if filled.cfg.PollInterval != time.Second || filled.cfg.BatchSize != defaultProxyLatencyBatchSize {
		t.Fatalf("默认 poll/batch=%v/%d", filled.cfg.PollInterval, filled.cfg.BatchSize)
	}
	if filled.cfg.Now == nil || filled.logger == nil {
		t.Fatal("Now/logger 必须回填默认实现")
	}
}

func TestWFResultProjectorCheckContractSQLite(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	if err := projector.CheckContract(context.Background()); err != nil {
		t.Fatalf("契约检查必须通过: %v", err)
	}
	if _, err := business.Exec(`DROP TABLE proxy_profiles`); err != nil {
		t.Fatalf("准备缺表失败: %v", err)
	}
	err := projector.CheckContract(context.Background())
	if err == nil || !strings.Contains(err.Error(), "契约不满足") {
		t.Fatalf("缺表必须报契约错误 err=%v", err)
	}
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

func TestWFProjectorDrainLifecycle(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()

	// 空库：Drain 必须返回 0 且不写 cursor。
	count, err := projector.Drain(ctx)
	if err != nil || count != 0 {
		t.Fatalf("空库 Drain count=%d err=%v", count, err)
	}
	var cursorCount int
	if err := business.QueryRow(`SELECT COUNT(*) FROM proxy_latency_projection_cursors`).Scan(&cursorCount); err != nil || cursorCount != 0 {
		t.Fatalf("空库不得写 cursor count=%d err=%v", cursorCount, err)
	}

	// 提交一条 committed outcome 并投影：applied + cursor 前进。
	wfSeedProxyRow(t, business, "p-1", wfProjRevision, nil)
	committer := newWFCommitter(t, store)
	outcome := committer.commit("p-1", wfProjRevision)
	count, err = projector.Drain(ctx)
	if err != nil || count != 1 {
		t.Fatalf("Drain count=%d err=%v", count, err)
	}
	status, latency, message, testedAt := wfBusinessProxyState(t, business, "p-1")
	if status != string(OverallPassed) {
		t.Fatalf("投影状态=%q want passed", status)
	}
	if !latency.Valid || latency.Int64 != 12 {
		t.Fatalf("投影延迟=%v want 12", latency)
	}
	if message.String != "代理质量检测通过" {
		t.Fatalf("投影消息=%q", message.String)
	}
	if want := outcome.ObservedAt.UTC().Format(time.RFC3339Nano); !testedAt.Valid || testedAt.String != want {
		t.Fatalf("last_tested_at=%v want %q", testedAt, want)
	}
	var disposition, receiptProxy string
	var reason sql.NullString
	if err := business.QueryRow(`SELECT disposition, proxy_id, reason FROM proxy_latency_projection_receipts WHERE outcome_id=?`, outcome.OutcomeID).
		Scan(&disposition, &receiptProxy, &reason); err != nil || disposition != string(ProjectionApplied) || receiptProxy != "p-1" || reason.Valid {
		t.Fatalf("receipt=%s/%s/%v err=%v", disposition, receiptProxy, reason, err)
	}
	var storedAt, outcomeID sql.NullString
	if err := business.QueryRow(`SELECT stored_at, outcome_id FROM proxy_latency_projection_cursors WHERE consumer_key='wf-consumer'`).
		Scan(&storedAt, &outcomeID); err != nil || !outcomeID.Valid || outcomeID.String != outcome.OutcomeID {
		t.Fatalf("cursor=%v/%v err=%v", storedAt, outcomeID, err)
	}

	// cursor 已推进：再次 Drain 不重复消费。
	count, err = projector.Drain(ctx)
	if err != nil || count != 0 {
		t.Fatalf("重放 Drain count=%d err=%v", count, err)
	}
}

func TestWFProjectorDrainRejectionSkipsPoisonRow(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()

	// W5 毒丸修复：items 为空的 committed outcome 是确定性 rejected——
	// receipt 落库后同事务越过游标，Drain 不再永久 fail-closed 重放。
	committer := newWFCommitter(t, store)
	outcome := committer.commitFunc("p-1", wfProjRevision, func(value *Outcome) {
		value.Items = nil
		value.OverallStatus = OverallUnknown
	})
	count, err := projector.Drain(ctx)
	if err != nil || count != 1 {
		t.Fatalf("确定性拒绝必须越过毒丸行 count=%d err=%v", count, err)
	}
	var disposition, reason string
	if err := business.QueryRow(`SELECT disposition, reason FROM proxy_latency_projection_receipts WHERE outcome_id=?`, outcome.OutcomeID).
		Scan(&disposition, &reason); err != nil || disposition != string(ProjectionRejected) || reason != "outcome_items_missing" {
		t.Fatalf("拒绝 receipt=%s/%s err=%v", disposition, reason, err)
	}
	var storedAt, outcomeID sql.NullString
	if err := business.QueryRow(`SELECT stored_at, outcome_id FROM proxy_latency_projection_cursors WHERE consumer_key='wf-consumer'`).
		Scan(&storedAt, &outcomeID); err != nil || !outcomeID.Valid || outcomeID.String != outcome.OutcomeID {
		t.Fatalf("rejected 行必须推进 cursor=%v/%v err=%v", storedAt, outcomeID, err)
	}
	if got := projector.RejectedSkippedCount(); got != 1 {
		t.Fatalf("RejectedSkippedCount=%d want 1", got)
	}
	// 游标已越过：再次 Drain 不再重放毒丸行。
	count, err = projector.Drain(ctx)
	if err != nil || count != 0 {
		t.Fatalf("越过后重放 Drain count=%d err=%v", count, err)
	}
}

func TestWFProjectorDrainStaleAndMissingDispositions(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()

	// 围栏行 revision 不匹配 → stale；代理缺失 → ignored；两者都必须
	// 记录 receipt 并让本轮 Drain 正常结束。
	pastRevision := wfProjBase.Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	wfSeedProxyRow(t, business, "p-stale", pastRevision, nil)
	committer := newWFCommitter(t, store)
	committer.commit("p-stale", wfProjRevision)
	committer.commit("p-missing", wfProjRevision)
	count, err := projector.Drain(ctx)
	if err != nil || count != 2 {
		t.Fatalf("Drain count=%d err=%v", count, err)
	}
	var staleReason string
	if err := business.QueryRow(`SELECT reason FROM proxy_latency_projection_receipts WHERE proxy_id='p-stale'`).Scan(&staleReason); err != nil || staleReason != "config_revision_stale" {
		t.Fatalf("stale reason=%q err=%v", staleReason, err)
	}
	var ignoredReason string
	if err := business.QueryRow(`SELECT reason FROM proxy_latency_projection_receipts WHERE proxy_id='p-missing'`).Scan(&ignoredReason); err != nil || ignoredReason != "proxy_missing_or_deleted" {
		t.Fatalf("ignored reason=%q err=%v", ignoredReason, err)
	}
}

func TestWFProjectorDrainObservedAtStale(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()

	// 业务库 last_tested_at 晚于 outcome 观测时间 → observed_at_stale。
	future := wfProjBase.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	wfSeedProxyRow(t, business, "p-1", wfProjRevision, future)
	committer := newWFCommitter(t, store)
	outcome := committer.commit("p-1", wfProjRevision)
	count, err := projector.Drain(ctx)
	if err != nil || count != 1 {
		t.Fatalf("Drain count=%d err=%v", count, err)
	}
	var reason string
	if err := business.QueryRow(`SELECT reason FROM proxy_latency_projection_receipts WHERE outcome_id=?`, outcome.OutcomeID).Scan(&reason); err != nil || reason != "observed_at_stale" {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
}

func TestWFProjectorProjectOutcome(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()

	// 未提交的 outcome 直接失败。
	ghost := testOutcome("outcome-ghost", "request-ghost", "p-ghost", OwnerLease{OwnerID: "o", FenceToken: 1}, ProxyLease{ProxyID: "p-ghost", OwnerID: "o", FenceToken: 1})
	if _, err := projector.ProjectOutcome(ctx, ghost); err == nil || !strings.Contains(err.Error(), "未找到匹配") {
		t.Fatalf("未提交 outcome err=%v", err)
	}

	wfSeedProxyRow(t, business, "p-1", wfProjRevision, nil)
	committer := newWFCommitter(t, store)
	outcome := committer.commit("p-1", wfProjRevision)

	// 身份不匹配（RequestID 改写）必须 fail closed。
	mismatched := outcome
	mismatched.RequestID = "request-other"
	if _, err := projector.ProjectOutcome(ctx, mismatched); err == nil || !strings.Contains(err.Error(), "未找到匹配") {
		t.Fatalf("身份不匹配 err=%v", err)
	}

	// 成功投影 + Ready 翻转。
	result, err := projector.ProjectOutcome(ctx, outcome)
	if err != nil || result.Disposition != ProjectionApplied || !result.Changed {
		t.Fatalf("投影结果=%+v err=%v", result, err)
	}
	if !projector.Ready() {
		t.Fatal("成功投影后必须 Ready")
	}

	// receipt 重放：同一 outcome 再次投影返回既有 receipt，不重复 CAS。
	replay, err := projector.ProjectOutcome(ctx, outcome)
	if err != nil || replay.Disposition != ProjectionApplied || replay.Changed {
		t.Fatalf("重放结果=%+v err=%v", replay, err)
	}

	// 投影失败 → Ready 翻转为 false。
	if _, err := business.Exec(`DROP TABLE proxy_latency_projection_receipts`); err != nil {
		t.Fatalf("准备失败路径: %v", err)
	}
	if _, err := projector.ProjectOutcome(ctx, outcome); err == nil {
		t.Fatal("receipt 表缺失必须报错")
	}
	if projector.Ready() {
		t.Fatal("失败后不得 Ready")
	}
}

func TestWFProjectorReadyGuards(t *testing.T) {
	var nilProjector *ResultProjector
	if nilProjector.Ready() {
		t.Fatal("nil projector 不得 Ready")
	}
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	if projector.Ready() {
		t.Fatal("从未成功投影不得 Ready")
	}
	projector.record(errors.New("历史错误"))
	if projector.Ready() {
		t.Fatal("存在错误不得 Ready")
	}
	projector.record(nil)
	if !projector.Ready() {
		t.Fatal("成功后必须 Ready")
	}
	// 时钟推进超过 2*interval+1s → 不再 Ready。
	projector.cfg.Now = func() time.Time { return wfProjBase.Add(3 * time.Minute) }
	if projector.Ready() {
		t.Fatal("超过容忍窗口不得 Ready")
	}
}

func TestWFProjectorProjectManualNoTargets(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "p-1", ProxyName: "代理一", ConfigRevision: wfProjRevision,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
	}
	observedAt := wfProjBase

	if _, err := projector.ProjectManualNoTargets(ctx, request, time.Time{}); err == nil {
		t.Fatal("零观测时间必须拒绝")
	}
	badRequest := request
	badRequest.SchemaVersion = 2
	if _, err := projector.ProjectManualNoTargets(ctx, badRequest, observedAt); err == nil {
		t.Fatal("非法请求必须拒绝")
	}
	withTargets := request
	withTargets.Targets = []ManualTarget{{Provider: "gpt", ProfileID: "p", Name: "n", URL: "https://api.openai.com/v1"}}
	if _, err := projector.ProjectManualNoTargets(ctx, withTargets, observedAt); err == nil {
		t.Fatal("带 targets 的请求必须拒绝")
	}

	// 代理缺失 → ignored。
	result, err := projector.ProjectManualNoTargets(ctx, request, observedAt)
	if err != nil || result.Disposition != ProjectionIgnored || result.Reason != "proxy_missing_or_deleted" || result.Changed {
		t.Fatalf("缺失结果=%+v err=%v", result, err)
	}

	// revision 不匹配 → stale。
	wfSeedProxyRow(t, business, "p-1", "2026-08-31T00:00:00Z", nil)
	result, err = projector.ProjectManualNoTargets(ctx, request, observedAt)
	if err != nil || result.Disposition != ProjectionStale || result.Reason != "config_revision_stale" {
		t.Fatalf("stale 结果=%+v err=%v", result, err)
	}

	// last_tested_at 晚于观测时间 → observed_at_stale。
	future := wfProjBase.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	wfSeedProxyRow(t, business, "p-1", wfProjRevision, future)
	result, err = projector.ProjectManualNoTargets(ctx, request, observedAt)
	if err != nil || result.Disposition != ProjectionStale || result.Reason != "observed_at_stale" {
		t.Fatalf("观测时间 stale 结果=%+v err=%v", result, err)
	}

	// 围栏通过但 CAS 未命中（updated_at 同时刻不同文本）→ CAS miss。
	granular := wfProjRevisionGranular
	wfSeedProxyRow(t, business, "p-1", granular, nil)
	result, err = projector.ProjectManualNoTargets(ctx, request, observedAt)
	if err != nil || result.Disposition != ProjectionStale || result.Reason != "projection_compare_and_set_missed" {
		t.Fatalf("CAS miss 结果=%+v err=%v", result, err)
	}

	// 正常应用：写 unknown 状态。
	wfSeedProxyRow(t, business, "p-1", wfProjRevision, nil)
	result, err = projector.ProjectManualNoTargets(ctx, request, observedAt)
	if err != nil || result.Disposition != ProjectionApplied || !result.Changed {
		t.Fatalf("应用结果=%+v err=%v", result, err)
	}
	status, latency, message, testedAt := wfBusinessProxyState(t, business, "p-1")
	if status != string(OverallUnknown) || latency.Valid {
		t.Fatalf("应用状态=%s latency=%v", status, latency)
	}
	if message.String != "代理检测未形成有效传输尝试" {
		t.Fatalf("应用消息=%q", message.String)
	}
	if !testedAt.Valid {
		t.Fatal("应用后 last_tested_at 必须写入")
	}
}

func TestWFProjectorProjectManualOutbound(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx := context.Background()
	observedAt := wfProjBase
	outcome := Outcome{ProxyID: "p-1", ConfigRevision: wfProjRevision, ObservedAt: observedAt}

	// 无诊断字段：直接 no-op，不触碰数据库。
	if err := projector.ProjectManualOutbound(ctx, outcome, "  ", ""); err != nil {
		t.Fatalf("空诊断必须 no-op: %v", err)
	}

	wfSeedProxyRow(t, business, "p-1", wfProjRevision, observedAt.UTC().Format(time.RFC3339Nano))
	if err := projector.ProjectManualOutbound(ctx, outcome, "1.2.3.4", "日本"); err != nil {
		t.Fatalf("双字段写入失败: %v", err)
	}
	var outboundIP, outboundRegion sql.NullString
	if err := business.QueryRow(`SELECT outbound_ip, outbound_region FROM proxy_profiles WHERE id='p-1'`).Scan(&outboundIP, &outboundRegion); err != nil {
		t.Fatalf("读取出口字段失败: %v", err)
	}
	if !outboundIP.Valid || outboundIP.String != "1.2.3.4" || !outboundRegion.Valid || outboundRegion.String != "日本" {
		t.Fatalf("出口=%v/%v", outboundIP, outboundRegion)
	}

	if err := projector.ProjectManualOutbound(ctx, outcome, "5.6.7.8", ""); err != nil {
		t.Fatalf("仅 IP 写入失败: %v", err)
	}
	if err := projector.ProjectManualOutbound(ctx, outcome, "", "美国"); err != nil {
		t.Fatalf("仅地区写入失败: %v", err)
	}

	// 代理缺失。
	missing := outcome
	missing.ProxyID = "p-missing"
	if err := projector.ProjectManualOutbound(ctx, missing, "1.2.3.4", ""); !errors.Is(err, ErrManualProxyMissing) {
		t.Fatalf("缺失代理 err=%v", err)
	}

	// revision 不匹配 → stale。
	stale := outcome
	stale.ConfigRevision = wfProjBase.Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if err := projector.ProjectManualOutbound(ctx, stale, "1.2.3.4", ""); !errors.Is(err, ErrManualProjectionStale) {
		t.Fatalf("stale err=%v", err)
	}

	// last_tested_at 不匹配 → stale。
	other := wfProjBase.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := business.Exec(`UPDATE proxy_profiles SET last_tested_at=? WHERE id='p-1'`, other); err != nil {
		t.Fatalf("改写 last_tested_at 失败: %v", err)
	}
	if err := projector.ProjectManualOutbound(ctx, outcome, "1.2.3.4", "德国"); !errors.Is(err, ErrManualProjectionStale) {
		t.Fatalf("last_tested 不匹配 err=%v", err)
	}

	// 围栏通过但 CAS 未命中（同时刻不同文本）。
	granular := wfProjRevisionGranular
	wfSeedProxyRow(t, business, "p-1", granular, observedAt.UTC().Format(time.RFC3339Nano))
	if err := projector.ProjectManualOutbound(ctx, outcome, "9.9.9.9", ""); err == nil || !strings.Contains(err.Error(), "CAS 未命中") {
		t.Fatalf("CAS 未命中 err=%v", err)
	}
}

func TestWFProjectorRunStopsOnCancelledContext(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := projector.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run 错误=%v，必须返回 context.Canceled", err)
	}
	var nilProjector *ResultProjector
	if err := nilProjector.Run(context.Background()); err == nil {
		t.Fatal("nil projector Run 必须报错")
	}
}
