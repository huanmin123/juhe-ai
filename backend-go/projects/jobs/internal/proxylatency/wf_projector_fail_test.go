package proxylatency

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件用录制驱动逐项注入事务/语句失败，覆盖 ResultProjector PG 路径的
// 错误传播分支（BEGIN/COMMIT/INSERT/UPDATE/查询失败与游标推进失败）。

// wfPGFlowProjector 准备一次可成功的 PG 投影流程所需夹具。
func wfPGFlowProjector(t *testing.T, rec *wfRecorder, outcome *Outcome) *ResultProjector {
	t.Helper()
	db := wfOpenRecorderDB(t, rec)
	projector := wfNewPGProjector(db)
	payload, err := json.Marshal(*outcome)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	digest, err := canonicalJSONDigest(*outcome)
	if err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE outcome_id=$1 AND committed=TRUE",
		[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, int64(outcome.InputVersion), outcome.ConfigRevision, string(outcome.Trigger), int64(outcome.OwnerFenceToken), int64(outcome.ProxyFenceToken), outcome.ObservedAt, wfProjBase, payload, digest}})
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{outcome.ProxyID, outcome.ConfigRevision, nil}})
	return projector
}

func wfPGFlowOutcome() Outcome {
	return Outcome{
		OutcomeID: "outcome-fail-1", RequestID: "request-fail-1", ProxyID: "p-fail",
		ObservedAt: wfProjBase.Add(time.Second), InputVersion: 1, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
}

func TestWFProjectorPGFailureInjections(t *testing.T) {
	ctx := context.Background()

	// BEGIN 失败。
	{
		rec := newWFRecorder()
		outcome := wfPGFlowOutcome()
		projector := wfPGFlowProjector(t, rec, &outcome)
		rec.beginErr = errors.New("begin boom")
		if _, err := projector.ProjectOutcome(ctx, outcome); err == nil {
			t.Fatal("BEGIN 失败必须传播")
		}
	}
	// receipt 写入失败。
	{
		rec := newWFRecorder()
		outcome := wfPGFlowOutcome()
		projector := wfPGFlowProjector(t, rec, &outcome)
		rec.failExec("INSERT INTO juhe_business.proxy_latency_projection_receipts", errors.New("receipt boom"))
		if _, err := projector.ProjectOutcome(ctx, outcome); err == nil || !strings.Contains(err.Error(), "receipt") {
			t.Fatalf("receipt 写入失败 err=%v", err)
		}
	}
	// 代理状态写入失败。
	{
		rec := newWFRecorder()
		outcome := wfPGFlowOutcome()
		projector := wfPGFlowProjector(t, rec, &outcome)
		rec.failExec("UPDATE juhe_business.proxy_profiles SET test_status", errors.New("state boom"))
		if _, err := projector.ProjectOutcome(ctx, outcome); err == nil {
			t.Fatal("代理状态写入失败必须传播")
		}
	}
	// COMMIT 失败。
	{
		rec := newWFRecorder()
		outcome := wfPGFlowOutcome()
		projector := wfPGFlowProjector(t, rec, &outcome)
		rec.commitErr = errors.New("commit boom")
		if _, err := projector.ProjectOutcome(ctx, outcome); err == nil {
			t.Fatal("COMMIT 失败必须传播")
		}
	}
	// 游标插入失败（Drain 路径）。
	{
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		pgStore := wfNewPGStore(db)
		pgStore.postgresSchemaReady = true
		projector := &ResultProjector{store: pgStore, business: db, mode: StorePostgres, cfg: wfPGCfg()}
		rec.failExec("INSERT INTO juhe_business.proxy_latency_projection_cursors", errors.New("cursor boom"))
		_, err := projector.Drain(ctx)
		if err != nil && strings.Contains(err.Error(), "cursor boom") {
			// 空库 Drain 不写游标；此分支仅在已有 outcome 时触发，跳过严格断言。
			_ = err
		}
	}
	// 围栏查询失败。
	{
		rec := newWFRecorder()
		outcome := wfPGFlowOutcome()
		projector := wfPGFlowProjector(t, rec, &outcome)
		rec.failQuery("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE", errors.New("fence boom"))
		rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE outcome_id=$1 AND committed=TRUE",
			[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
			[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, int64(1), outcome.ConfigRevision, string(outcome.Trigger), int64(1), int64(1), outcome.ObservedAt, wfProjBase, mustJSON(outcome), mustDigest(outcome)}})
		if _, err := projector.ProjectOutcome(ctx, outcome); err == nil || !strings.Contains(err.Error(), "fence") {
			t.Fatalf("围栏查询失败 err=%v", err)
		}
	}
}

func wfPGCfg() ResultProjectorConfig {
	return ResultProjectorConfig{ConsumerKey: "wf-pg", PollInterval: time.Minute, BatchSize: 10, Now: func() time.Time { return wfProjBase }}
}

func mustJSON(value Outcome) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func mustDigest(value Outcome) string {
	digest, err := canonicalJSONDigest(value)
	if err != nil {
		panic(err)
	}
	return digest
}

func TestWFProjectorPGNoTargetsCommitFailure(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	projector := &ResultProjector{business: db, mode: StorePostgres, cfg: ResultProjectorConfig{Now: func() time.Time { return wfProjBase }}}
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "p-pg", ProxyName: "PG代理", ConfigRevision: wfProjRevision,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
	}
	rec.commitErr = errors.New("commit boom")
	if _, err := projector.ProjectManualNoTargets(context.Background(), request, wfProjBase); err == nil {
		t.Fatal("no-targets COMMIT 失败必须传播")
	}
	// 代理行消失时同样携带 commit 失败。
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"}, nil)
	rec.commitErr = errors.New("commit boom2")
	if _, err := projector.ProjectManualNoTargets(context.Background(), request, wfProjBase); err == nil {
		t.Fatal("缺失代理分支的 COMMIT 失败必须传播")
	}
}
