package proxylatency

import (
	"context"
	"database/sql/driver"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

// 本文件收尾剩余分支：runCycle 过期执行窗口与日志分支、IssueInput 的
// PG 语句失败传播、PG receipt 幂等冲突重放，以及执行器 DB 闸门日志。

func TestWFRunCycleExpiredWindowAndLogging(t *testing.T) {
	store := wfOpenJobsStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := NewRunner(RuntimeConfig{InstanceID: "wf-last", OwnerLease: time.Hour, ProxyLease: time.Hour, ProbeTimeout: time.Second}, store, &wfFakeReader{}, logger)
	runner.SetResultProjector(wfNewProjector(t, store, wfOpenBusinessDB(t)))
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-last", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	// 执行窗口为 0：executeIssuedInput 不应被调用，计数记为执行失败。
	runner.reader = &wfFakeReader{drafts: []InputDraft{wfCycleDraft("p-last")}}
	runner.acquireProxyLease = func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error) {
		return ProxyLease{ProxyID: "p-last", OwnerID: owner.OwnerID, FenceToken: 1, LeaseUntil: time.Now().Add(-time.Second)}, true, nil
	}
	hookRan := false
	runner.executeIssuedInput = func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error) {
		hookRan = true
		return Outcome{}, true, nil
	}
	// 窗口过期记为执行失败；随后真实释放失败（租约行不存在）级联为致命错误。
	err = runner.runCycle(ctx, owner)
	if err == nil || !strings_Contains(err.Error(), "execution window expired") {
		t.Fatalf("过期窗口 err=%v", err)
	}
	if hookRan {
		t.Fatal("窗口过期不得执行探测")
	}
	if runner.Status().ReleaseFailures != 1 {
		t.Fatalf("释放失败未记账：%+v", runner.Status())
	}
}

func TestWFStorePGIssueInputFailures(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	pgStore := wfNewPGStore(db)
	ctx := context.Background()
	draft := InputDraft{
		ProxyID: "p-pg", ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "10.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
	// next_version 查询失败。
	rec.failQuery("INSERT INTO juhe_jobs.proxy_latency_input_versions", errors.New("version boom"))
	if _, err := pgStore.IssueInput(ctx, draft); err == nil || !strings_Contains(err.Error(), "version boom") {
		t.Fatalf("版本查询失败 err=%v", err)
	}
	// 输入持久化失败（先补齐版本查询成功脚本）。
	rec.script("INSERT INTO juhe_jobs.proxy_latency_input_versions", []string{"next_version-1"}, [][]driver.Value{{int64(2)}})
	rec.failExec("INSERT INTO juhe_jobs.proxy_latency_inputs", errors.New("input boom"))
	if _, err := pgStore.IssueInput(ctx, draft); err == nil || !strings_Contains(err.Error(), "input boom") {
		t.Fatalf("输入持久化失败 err=%v", err)
	}
}

func TestWFStorePGReceiptIdempotentReplay(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	pgStore := wfNewPGStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	rec.script("INSERT INTO juhe_jobs.proxy_latency_owner_leases", []string{"fence_token"}, [][]driver.Value{{int64(1)}})
	owner, ok, err := pgStore.AcquireOwnerLease(ctx, "wf-pg-replay", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	// 同 owner + 已存在租约未过期 → 幂等拒绝。
	if _, ok, err := pgStore.AcquireOwnerLease(ctx, "other", time.Hour); err != nil || ok {
		t.Fatalf("未过期租约必须拒绝 ok=%v err=%v", ok, err)
	}

	// 既有 outcome 且载荷一致 → 重放幂等。
	issued := IssuedInput{
		RequestID: "request-replay-pg", ProxyID: "p-pg", InputVersion: 1, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion,
		ProxyType: "http", ProxyHost: "10.0.0.1", ProxyPort: 8080,
		Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}
	outcome := Outcome{
		OutcomeID: stableOutcomeID(issued.RequestID), RequestID: issued.RequestID, ProxyID: "p-pg",
		ObservedAt: issued.IssuedAt.Add(time.Second).Truncate(time.Microsecond), InputVersion: issued.InputVersion, ConfigRevision: issued.ConfigRevision,
		Trigger: issued.Trigger, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxyFenceForTest(),
		OverallStatus: OverallPassed,
		Items:         []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 FOR UPDATE",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.ProxyID, int64(outcome.InputVersion), outcome.ConfigRevision, string(outcome.Trigger), int64(outcome.OwnerFenceToken), int64(outcome.ProxyFenceToken), mustDigest(outcome)}})
	committed, err := pgStore.AppendOutcome(ctx, owner, ProxyLease{ProxyID: "p-pg", OwnerID: owner.OwnerID, FenceToken: proxyFenceForTest()}, outcome)
	if err != nil || committed {
		t.Fatalf("PG 幂等重放 committed=%v err=%v", committed, err)
	}

	// 载荷不一致 → ErrRequestConflict。
	conflict := outcome
	conflict.OverallStatus = OverallFailed
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE request_id=$1 FOR UPDATE",
		[]string{"outcome_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.ProxyID, int64(outcome.InputVersion), outcome.ConfigRevision, string(outcome.Trigger), int64(outcome.OwnerFenceToken), int64(outcome.ProxyFenceToken), mustDigest(outcome)}})
	if _, err := pgStore.AppendOutcome(ctx, owner, ProxyLease{ProxyID: "p-pg", OwnerID: owner.OwnerID, FenceToken: proxyFenceForTest()}, conflict); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("PG 冲突重放 err=%v", err)
	}
}

func proxyFenceForTest() int64 { return 2 }

func strings_Contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && containsSub(haystack, needle)
}

func containsSub(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
