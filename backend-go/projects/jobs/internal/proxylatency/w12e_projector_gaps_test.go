package proxylatency

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// w12e_projector_gaps_test.go 补齐 ResultProjector 剩余臂：Drain/
// ProjectOutcome/ProjectManualNoTargets/ProjectManualOutbound 的逐阶段失败、
// advanceCursorTx 全部分支、manualOutboundUpdate 的 PG 语句族。全部基于
// wf 录制驱动，不依赖真实数据库。

// w12ePGDigest 计算 outcome 的 canonical JSON digest（PG 校验口径）。
func w12ePGDigest(t *testing.T, outcome Outcome) (payload []byte, digest string) {
	t.Helper()
	payload, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:])
}

func w12eProjectionOutcome(requestID, proxyID, revision string, observed time.Time) Outcome {
	return Outcome{
		OutcomeID: stableOutcomeID(requestID), RequestID: requestID, ProxyID: proxyID,
		ObservedAt: observed, InputVersion: 1, ConfigRevision: revision, Trigger: TriggerPeriodic,
		OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "openai", ProfileID: "profile", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
}

// w12eScriptCommittedList 登记一条可被 ListCommittedOutcomes 读到的行。
func w12eScriptCommittedList(t *testing.T, rec *wfRecorder, outcome Outcome, storedAt time.Time) {
	t.Helper()
	payload, digest := w12ePGDigest(t, outcome)
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE committed=TRUE",
		[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.OwnerFenceToken, outcome.ProxyFenceToken, outcome.ObservedAt, storedAt, payload, digest}})
}

func w12eScriptFenceMatch(rec *wfRecorder, proxyID, revision string, lastTestedAt any) {
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{proxyID, revision, lastTestedAt}})
}

// TestW12EProjectorGuards：未初始化守卫与 record nil 保护。
func TestW12EProjectorGuards(t *testing.T) {
	ctx := context.Background()
	empty := &ResultProjector{}
	if _, err := empty.ProjectOutcome(ctx, Outcome{}); err == nil {
		t.Fatalf("ProjectOutcome 未初始化应报错")
	}
	// business 为 nil 或出站信息全空都走静默跳过（生产语义即返回 nil）。
	if err := empty.ProjectManualOutbound(ctx, Outcome{}, "1.2.3.4", ""); err != nil {
		t.Fatalf("business 缺失应静默跳过: %v", err)
	}
	if _, err := empty.ProjectManualNoTargets(ctx, ManualRequest{}, time.Now()); err == nil {
		t.Fatalf("ProjectManualNoTargets 未初始化应报错")
	}
	var nilProjector *ResultProjector
	nilProjector.record(nil)
	if err := empty.ProjectManualOutbound(ctx, Outcome{}, " ", " "); err != nil {
		t.Fatalf("出站信息全空应直接跳过: %v", err)
	}
}

// TestW12EDrainArms：Drain 的列表失败臂与完整推进链。
func TestW12EDrainArms(t *testing.T) {
	ctx := context.Background()
	revision := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	outcome := w12eProjectionOutcome("j3a-w12e-drain", "w12e-p", revision, time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC))
	storedAt := time.Date(2026, 9, 10, 12, 2, 0, 0, time.UTC)
	// 列表失败。
	rec := newWFRecorder()
	rec.failQuery("FROM juhe_jobs.proxy_latency_outcomes WHERE committed=TRUE", errors.New("w12e list boom"))
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).Drain(ctx); err == nil {
		t.Fatalf("列表失败应透传")
	}
	// 完整推进链：receipt 缺失 → 围栏匹配 → 应用 → receipt → cursor 创建。
	rec = newWFRecorder()
	w12eScriptCommittedList(t, rec, outcome, storedAt)
	w12eScriptFenceMatch(rec, outcome.ProxyID, revision, nil)
	if n, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).Drain(ctx); err != nil || n != 1 {
		t.Fatalf("Drain 应推进 1 条: n=%d err=%v", n, err)
	}
}

// TestW12EProjectOutcomeArms：FindCommittedOutcome 失败 / rejected。
func TestW12EProjectOutcomeArms(t *testing.T) {
	ctx := context.Background()
	revision := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	outcome := w12eProjectionOutcome("j3a-w12e-po", "w12e-p", revision, time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC))
	// FindCommittedOutcome 查询失败。
	rec := newWFRecorder()
	rec.failQuery("WHERE outcome_id=$1 AND committed=TRUE", errors.New("w12e find boom"))
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectOutcome(ctx, outcome); err == nil {
		t.Fatalf("find 失败应透传")
	}
	// committed outcome items 缺失 → rejected。
	bad := outcome
	bad.Items = nil
	payload, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	rec = newWFRecorder()
	rec.script("WHERE outcome_id=$1 AND committed=TRUE",
		[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
		[][]driver.Value{{bad.OutcomeID, bad.RequestID, bad.ProxyID, bad.InputVersion, bad.ConfigRevision, string(bad.Trigger), bad.OwnerFenceToken, bad.ProxyFenceToken, bad.ObservedAt, time.Now(), payload, hex.EncodeToString(sum[:])}})
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{bad.ProxyID, revision, nil}})
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectOutcome(ctx, outcome); err == nil || !strings.Contains(err.Error(), "rejected outcome") {
		t.Fatalf("items 缺失应 rejected: %v", err)
	}
	// receipt 读取失败。
	rec = newWFRecorder()
	rec.failQuery("proxy_latency_projection_receipts", errors.New("w12e receipt boom"))
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectOutcome(ctx, outcome); err == nil {
		t.Fatalf("receipt 失败应透传")
	}
	// applyStateUpdate 执行失败。
	rec = newWFRecorder()
	committedPayload, committedDigest := w12ePGDigest(t, outcome)
	rec.script("WHERE outcome_id=$1 AND committed=TRUE",
		[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.OwnerFenceToken, outcome.ProxyFenceToken, outcome.ObservedAt, time.Now(), committedPayload, committedDigest}})
	w12eScriptFenceMatch(rec, outcome.ProxyID, revision, nil)
	rec.failExec("UPDATE juhe_business.proxy_profiles SET test_status", errors.New("w12e apply boom"))
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectOutcome(ctx, outcome); err == nil {
		t.Fatalf("apply 失败应透传")
	}
}

// TestW12EProjectManualNoTargetsArms：逐分支含 commit 失败。
func TestW12EProjectManualNoTargetsArms(t *testing.T) {
	ctx := context.Background()
	request := testManualReleaseRequest()
	request.Targets = nil
	observed := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	// 请求非法（revision 非规范）。
	invalid := request
	invalid.ConfigRevision = "garbage"
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, newWFRecorder())).ProjectManualNoTargets(ctx, invalid, observed); err == nil {
		t.Fatalf("非法请求应报错")
	}
	// observedAt 零值。
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, newWFRecorder())).ProjectManualNoTargets(ctx, request, time.Time{}); err == nil {
		t.Fatalf("零值 observed 应报错")
	}
	// 代理缺失 → Ignored。
	rec := newWFRecorder()
	if result, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err != nil || result.Disposition != ProjectionIgnored {
		t.Fatalf("代理缺失应 Ignored: %+v %v", result, err)
	}
	// revision 漂移 → Stale。
	rec = newWFRecorder()
	w12eScriptFenceMatch(rec, request.ProxyID, "2000-01-01T00:00:00.000000Z", nil)
	if result, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err != nil || result.Disposition != ProjectionStale {
		t.Fatalf("revision 漂移应 Stale: %+v %v", result, err)
	}
	// observed 漂移（库里 last_tested_at 更新）→ Stale。
	rec = newWFRecorder()
	w12eScriptFenceMatch(rec, request.ProxyID, request.ConfigRevision, observed.Add(time.Hour).Format("2006-01-02T15:04:05.999999Z"))
	if result, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err != nil || result.Reason != "observed_at_stale" {
		t.Fatalf("observed 漂移应 stale: %+v %v", result, err)
	}
	// 应用成功 → Applied。
	rec = newWFRecorder()
	w12eScriptFenceMatch(rec, request.ProxyID, request.ConfigRevision, nil)
	if result, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err != nil || result.Disposition != ProjectionApplied {
		t.Fatalf("应 Applied: %+v %v", result, err)
	}
	// 各分支 commit 失败。
	rec = newWFRecorder()
	rec.commitErr = errors.New("w12e commit")
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err == nil || !strings.Contains(err.Error(), "提交") {
		t.Fatalf("missing 分支 commit 失败应包装: %v", err)
	}
	rec = newWFRecorder()
	w12eScriptFenceMatch(rec, request.ProxyID, "2000-01-01T00:00:00.000000Z", nil)
	rec.commitErr = errors.New("w12e commit2")
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err == nil || !strings.Contains(err.Error(), "提交") {
		t.Fatalf("stale revision 分支 commit 失败应包装: %v", err)
	}
	rec = newWFRecorder()
	w12eScriptFenceMatch(rec, request.ProxyID, request.ConfigRevision, nil)
	rec.commitErr = errors.New("w12e commit3")
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err == nil || !strings.Contains(err.Error(), "提交") {
		t.Fatalf("applied 分支 commit 失败应包装: %v", err)
	}
	// applyStateUpdate 执行失败。
	rec = newWFRecorder()
	w12eScriptFenceMatch(rec, request.ProxyID, request.ConfigRevision, nil)
	rec.failExec("UPDATE juhe_business.proxy_profiles SET test_status", errors.New("w12e apply"))
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err == nil {
		t.Fatalf("apply 失败应透传")
	}
	// 围栏查询失败。
	rec = newWFRecorder()
	rec.failQuery("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE", errors.New("w12e fence"))
	if _, err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualNoTargets(ctx, request, observed); err == nil {
		t.Fatalf("围栏失败应透传")
	}
}

// TestW12EProjectManualOutboundArms：出站投影的逐阶段失败与成功。
func TestW12EProjectManualOutboundArms(t *testing.T) {
	ctx := context.Background()
	revision := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	observed := time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC)
	outcome := w12eProjectionOutcome("j3a-w12e-out", "w12e-p", revision, observed)
	build := func() *ResultProjector { return wfNewPGProjector(wfOpenRecorderDB(t, newWFRecorder())) }
	// begin 失败。
	rec := newWFRecorder()
	rec.beginErr = errors.New("w12e begin")
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); err == nil {
		t.Fatalf("begin 失败应透传")
	}
	// exec 失败。
	rec = newWFRecorder()
	rec.failExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", errors.New("w12e outbound boom"))
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); err == nil {
		t.Fatalf("exec 失败应透传")
	}
	// CAS 未命中 + 围栏查询失败。
	rec = newWFRecorder()
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	rec.failQuery("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE", errors.New("w12e fence boom"))
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); err == nil {
		t.Fatalf("围栏失败应透传")
	}
	// CAS 未命中 + 代理缺失（commit 失败 / 成功）。
	rec = newWFRecorder()
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	rec.commitErr = errors.New("w12e missing commit")
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); err == nil {
		t.Fatalf("missing commit 失败应透传")
	}
	rec = newWFRecorder()
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); !errors.Is(err, ErrManualProxyMissing) {
		t.Fatalf("代理缺失应 ErrManualProxyMissing: %v", err)
	}
	// CAS 未命中 + revision 漂移（commit 失败 / 成功）。
	rec = newWFRecorder()
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	w12eScriptFenceMatch(rec, outcome.ProxyID, "2000-01-01T00:00:00.000000Z", nil)
	rec.commitErr = errors.New("w12e stale commit")
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); err == nil {
		t.Fatalf("stale commit 失败应透传")
	}
	rec = newWFRecorder()
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	w12eScriptFenceMatch(rec, outcome.ProxyID, "2000-01-01T00:00:00.000000Z", nil)
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); !errors.Is(err, ErrManualProjectionStale) {
		t.Fatalf("revision 漂移应 ErrManualProjectionStale: %v", err)
	}
	// CAS 未命中且围栏一致 → 明确错误。
	rec = newWFRecorder()
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 0)
	w12eScriptFenceMatch(rec, outcome.ProxyID, revision, observed.UTC().Format("2006-01-02T15:04:05.999999Z"))
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); err == nil || !strings.Contains(err.Error(), "CAS 未命中") {
		t.Fatalf("CAS 未命中应报错: %v", err)
	}
	// 成功（IP+地区 / 仅 IP / 仅地区）。
	rec = newWFRecorder()
	rec.scriptExec("UPDATE juhe_business.proxy_profiles SET outbound_ip", 1)
	if err := wfNewPGProjector(wfOpenRecorderDB(t, rec)).ProjectManualOutbound(ctx, outcome, "1.2.3.4", "测试地区"); err != nil {
		t.Fatalf("IP+地区应成功: %v", err)
	}
	if err := build().ProjectManualOutbound(ctx, outcome, "1.2.3.4", ""); err != nil {
		t.Fatalf("仅 IP 应成功: %v", err)
	}
	if err := build().ProjectManualOutbound(ctx, outcome, "", "测试地区"); err != nil {
		t.Fatalf("仅地区应成功: %v", err)
	}
}

// TestW12EManualOutboundUpdateArms：PG/SQLite 各语句族。
func TestW12EManualOutboundUpdateArms(t *testing.T) {
	outcome := Outcome{ProxyID: "p", ConfigRevision: "r", ObservedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	pg := &ResultProjector{mode: StorePostgres}
	sqlite := &ResultProjector{mode: StoreSQLite}
	query, args := pg.manualOutboundUpdate(outcome, "1.2.3.4", " region ")
	if !strings.Contains(query, "outbound_ip=$1,outbound_region=$2") || len(args) != 5 {
		t.Fatalf("PG IP+地区语句错误: %s", query)
	}
	if query, _ := pg.manualOutboundUpdate(outcome, "1.2.3.4", ""); !strings.Contains(query, "outbound_ip=$1 WHERE") {
		t.Fatalf("PG 仅 IP 语句错误: %s", query)
	}
	if query, _ := pg.manualOutboundUpdate(outcome, "", "region"); !strings.Contains(query, "outbound_region=$1") {
		t.Fatalf("PG 仅地区语句错误: %s", query)
	}
	if query, _ := sqlite.manualOutboundUpdate(outcome, "1.2.3.4", "region"); !strings.Contains(query, "outbound_ip=?,outbound_region=?") {
		t.Fatalf("SQLite IP+地区语句错误: %s", query)
	}
	if query, _ := sqlite.manualOutboundUpdate(outcome, "1.2.3.4", ""); !strings.Contains(query, "outbound_ip=? WHERE") {
		t.Fatalf("SQLite 仅 IP 语句错误: %s", query)
	}
	if query, _ := sqlite.manualOutboundUpdate(outcome, "", "region"); !strings.Contains(query, "outbound_region=?") {
		t.Fatalf("SQLite 仅地区语句错误: %s", query)
	}
}

// TestW12EAdvanceCursorArms：游标推进的全部分支。
func TestW12EAdvanceCursorArms(t *testing.T) {
	ctx := context.Background()
	projector := wfNewPGProjector(wfOpenRecorderDB(t, newWFRecorder()))
	// 参数无效。
	if err := projector.advanceCursorTx(ctx, nil, OutcomeCursor{}); err == nil {
		t.Fatalf("空游标应报错")
	}
	buildStored := func(t *testing.T, rec *wfRecorder) (*ResultProjector, *sql.Tx, StoredOutcome, func()) {
		t.Helper()
		revision := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		outcome := w12eProjectionOutcome("j3a-w12e-cursor", "w12e-p", revision, time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC))
		w12eScriptCommittedList(t, rec, outcome, time.Date(2026, 9, 10, 12, 2, 0, 0, time.UTC))
		w12eScriptFenceMatch(rec, outcome.ProxyID, revision, nil)
		projector := wfNewPGProjector(wfOpenRecorderDB(t, rec))
		tx, err := projector.business.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		cleanup := func() { _ = tx.Rollback() }
		return projector, tx, StoredOutcome{Outcome: outcome, StoredAt: time.Date(2026, 9, 10, 12, 2, 0, 0, time.UTC)}, cleanup
	}
	// 游标不存在 → 创建（exec 失败 / 成功）。
	rec := newWFRecorder()
	projector, tx, stored, cleanup := buildStored(t, rec)
	rec.failExec("INSERT INTO juhe_business.proxy_latency_projection_cursors", errors.New("w12e cursor insert"))
	if err := projector.advanceCursorTx(ctx, tx, OutcomeCursor{StoredAt: stored.StoredAt, OutcomeID: stored.OutcomeID}); err == nil {
		t.Fatalf("游标创建失败应透传")
	}
	cleanup()
	// 游标存在且 stored_at 非法。
	rec = newWFRecorder()
	projector, tx, stored, cleanup = buildStored(t, rec)
	rec.script("FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE",
		[]string{"stored_at", "outcome_id"}, [][]driver.Value{{"garbage", "w12e-o"}})
	if err := projector.advanceCursorTx(ctx, tx, OutcomeCursor{StoredAt: stored.StoredAt, OutcomeID: stored.OutcomeID}); err == nil {
		t.Fatalf("stored_at 非法应报错")
	}
	cleanup()
	// 游标存在且合法 → UPDATE 执行失败 / 未命中 / 成功。
	rec = newWFRecorder()
	projector, tx, stored, cleanup = buildStored(t, rec)
	rec.script("FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE",
		[]string{"stored_at", "outcome_id"}, [][]driver.Value{{"2026-09-10T00:00:00.000000Z", "w12e-old"}})
	rec.failExec("UPDATE juhe_business.proxy_latency_projection_cursors SET stored_at", errors.New("w12e cursor update"))
	if err := projector.advanceCursorTx(ctx, tx, OutcomeCursor{StoredAt: stored.StoredAt, OutcomeID: stored.OutcomeID}); err == nil {
		t.Fatalf("游标更新失败应透传")
	}
	cleanup()
	rec = newWFRecorder()
	projector, tx, stored, cleanup = buildStored(t, rec)
	rec.script("FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE",
		[]string{"stored_at", "outcome_id"}, [][]driver.Value{{"2026-09-10T00:00:00.000000Z", "w12e-old"}})
	rec.scriptExec("UPDATE juhe_business.proxy_latency_projection_cursors SET stored_at", 0)
	if err := projector.advanceCursorTx(ctx, tx, OutcomeCursor{StoredAt: stored.StoredAt, OutcomeID: stored.OutcomeID}); err == nil || !strings.Contains(err.Error(), "未前进") {
		t.Fatalf("游标未前进应报错: %v", err)
	}
	cleanup()
	rec = newWFRecorder()
	projector, tx, stored, cleanup = buildStored(t, rec)
	rec.script("FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE",
		[]string{"stored_at", "outcome_id"}, [][]driver.Value{{"2026-09-10T00:00:00.000000Z", "w12e-old"}})
	if err := projector.advanceCursorTx(ctx, tx, OutcomeCursor{StoredAt: stored.StoredAt, OutcomeID: stored.OutcomeID}); err != nil {
		t.Fatalf("游标应前进: %v", err)
	}
	cleanup()
}

// TestW12EInsertReceiptInvalidDisposition：非法 disposition 的防御臂。
func TestW12EInsertReceiptInvalidDisposition(t *testing.T) {
	projector := wfNewPGProjector(wfOpenRecorderDB(t, newWFRecorder()))
	if err := projector.insertReceiptTx(context.Background(), nil, ProjectionResult{}, "weird", ""); err == nil {
		t.Fatalf("非法 disposition 应报错")
	}
}

// TestW12EApplyStateUpdateInvalidParams：参数校验臂。
func TestW12EApplyStateUpdateInvalidParams(t *testing.T) {
	projector := wfNewPGProjector(wfOpenRecorderDB(t, newWFRecorder()))
	disposition, err := projector.applyStateUpdate(context.Background(), nil, "p", "not-a-time", time.Now(), projectionSummary{Status: string(OverallPassed)})
	if disposition != ProjectionRejected || err == nil {
		t.Fatalf("非法 revision 应拒绝: %v %v", disposition, err)
	}
	disposition, err = projector.applyStateUpdate(context.Background(), nil, "p", time.Now().Format(time.RFC3339Nano), time.Now(), projectionSummary{Status: "weird"})
	if disposition != ProjectionRejected || err == nil {
		t.Fatalf("非法状态应拒绝: %v %v", disposition, err)
	}
	disposition, err = projector.applyStateUpdate(context.Background(), nil, "p", time.Now().Format(time.RFC3339Nano), time.Time{}, projectionSummary{Status: string(OverallPassed)})
	if disposition != ProjectionRejected || err == nil {
		t.Fatalf("零值时刻应拒绝: %v %v", disposition, err)
	}
}
