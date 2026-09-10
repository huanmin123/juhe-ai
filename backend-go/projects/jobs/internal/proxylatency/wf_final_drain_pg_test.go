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

func TestWFProjectorPGDrainAdvancesCursor(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	pgStore := wfNewPGStore(db)
	projector := &ResultProjector{store: pgStore, business: db, mode: StorePostgres, cfg: wfPGCfg()}
	ctx := context.Background()

	outcome := wfPGFlowOutcome()
	payload, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	digest, err := canonicalJSONDigest(outcome)
	if err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	// ListCommittedOutcomes 返回一条已提交 outcome。
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE committed=TRUE",
		[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, int64(1), outcome.ConfigRevision, string(outcome.Trigger), int64(1), int64(1), outcome.ObservedAt, wfProjBase, payload, digest}})
	// receipt 查询为空 → 全新投影；围栏命中；状态写入成功；receipt 插入成功。
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{outcome.ProxyID, outcome.ConfigRevision, nil}})
	// 游标已有更旧值 → UPDATE 前进。
	rec.script("FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE",
		[]string{"stored_at", "outcome_id"},
		[][]driver.Value{{wfProjBase.Add(-time.Hour), "outcome-old"}})

	count, err := projector.Drain(ctx)
	if err != nil || count != 1 {
		t.Fatalf("Drain count=%d err=%v", count, err)
	}
	joined := ""
	for _, statement := range rec.all() {
		joined += statement.query + "\n"
	}
	for _, required := range []string{
		"UPDATE juhe_business.proxy_latency_projection_cursors SET stored_at=$1,outcome_id=$2,updated_at=$3 WHERE consumer_key=$4",
		"SELECT stored_at,outcome_id FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("PG Drain 缺少 %q", required)
		}
	}

	// 游标倒退 → Drain 报错。
	rec2 := newWFRecorder()
	db2 := wfOpenRecorderDB(t, rec2)
	pgStore2 := wfNewPGStore(db2)
	projector2 := &ResultProjector{store: pgStore2, business: db2, mode: StorePostgres, cfg: wfPGCfg()}
	rec2.script("FROM juhe_jobs.proxy_latency_outcomes WHERE committed=TRUE",
		[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, int64(1), outcome.ConfigRevision, string(outcome.Trigger), int64(1), int64(1), outcome.ObservedAt, wfProjBase, payload, digest}})
	rec2.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{outcome.ProxyID, outcome.ConfigRevision, nil}})
	rec2.script("FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1 FOR UPDATE",
		[]string{"stored_at", "outcome_id"},
		[][]driver.Value{{wfProjBase.Add(time.Hour), "outcome-future"}})
	rec2.failExec("UPDATE juhe_business.proxy_latency_projection_cursors", errors.New("cursor boom"))
	if _, err := projector2.Drain(ctx); err == nil {
		t.Fatal("游标倒退必须报错")
	}
}
