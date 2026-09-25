package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// w9h_cmd_units_test.go 覆盖探针族适配器与恢复探针闭包的错误分支：
// 这些闭包经由 accountprobe.Service（sqlite 源）对缺失账户快速失败，
// 不发起上游 HTTP。

func w9hProbeStoreFixture(t *testing.T) (*proberepo.Store, *proberepoStoreHandle) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	seedProbeCoreTables(t, path)
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatal(err)
	}
	if err := probeStore.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	handle := &proberepoStoreHandle{db: db}
	return probeStore, handle
}

func w9hProbeService(t *testing.T, store *proberepo.Store) *accountprobe.Service {
	t.Helper()
	service, err := accountprobe.NewService(accountprobe.Options{Source: store, Secret: wgBalanceSecret, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestW9HSpeedFirstCandidateSourceArms(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	runtimeKey := wgSeedRecoveryAccount(t, handle)
	source := speedFirstCandidateSource{store: store}
	ctx := context.Background()

	// 命中账户：状态与可调度位透传。
	summary, err := source.FindAccountForTest(ctx, "acc-rt", runtimeKey)
	if err != nil || summary == nil {
		t.Fatalf("FindAccountForTest summary=%+v err=%v", summary, err)
	}
	if summary.Status != "active" || !summary.Schedulable {
		t.Fatalf("summary=%+v", summary)
	}
	// 缺失账户：nil, nil。
	missing, err := source.FindAccountForTest(ctx, "acc-absent", runtimeKey)
	if err != nil || missing != nil {
		t.Fatalf("missing summary=%+v err=%v", missing, err)
	}
	// 候选解析：命中与缺失。
	candidate, err := source.FindCandidateAccount(ctx, "g-rt", "acc-rt", "sys-rt")
	if err != nil || candidate == nil || candidate.AccountID != "acc-rt" || candidate.GroupID != "g-rt" {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	absentCandidate, err := source.FindCandidateAccount(ctx, "g-rt", "acc-absent", "sys-rt")
	if err != nil || absentCandidate != nil {
		t.Fatalf("absent candidate=%+v err=%v", absentCandidate, err)
	}
	// 到期列损坏（非 RFC3339）→ proberepo 按错误上抛（BUG-0180：原 panic
	// 会使长驻 probe worker 进入崩溃循环，由调用方按读取失败处理）。
	if _, err := handle.db.Exec(`UPDATE accounts SET account_expires_at = 'not-a-time' WHERE id = 'acc-rt'`); err != nil {
		t.Fatal(err)
	}
	if summary, err := source.FindAccountForTest(ctx, "acc-rt", runtimeKey); err == nil {
		t.Fatalf("损坏的到期时间必须被拒绝: summary=%+v", summary)
	}
}

func TestW9HSpeedFirstProbeFuncTaskFailureBranch(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	wgSeedRecoveryAccount(t, handle)
	closure := speedFirstProbeFunc(w9hProbeService(t, store))
	// 缺失候选账户：诊断任务快速失败 → snapshot 失败 + unknown/task_failure。
	snapshot, outcome := closure(context.Background(), nil,
		opsjobs.ProbeCandidate{
			AccountID: "acc-absent",
			Scope:     opsjobs.ProbeScope{GroupID: "g-rt", SystemAccountID: "sys-rt"},
		}, nil)
	if snapshot.Success {
		t.Fatalf("失败诊断不得返回成功快照: %+v", snapshot)
	}
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestW9HCircuitRecoveryTransportProbeTaskFailureBranch(t *testing.T) {
	store, handle := w9hProbeStoreFixture(t)
	runtimeKey := wgSeedRecoveryAccount(t, handle)
	_ = runtimeKey
	service := w9hProbeService(t, store)
	// 零值 scope 无 modelBucket → 不钉住（回退健康检查模型，既有行为）。
	outcome := circuitRecoveryTransportProbe(context.Background(), service,
		circuitRecoveryProbeRequest(
			opsjobs.RecoveryRuntimeIdentity{Kind: "owner", AccountID: "acc-absent", SystemAccountID: "sys-rt"},
			opsjobs.CircuitState{}, "g-rt", "sys-rt"))
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("outcome=%+v", outcome)
	}
}

// ---------------------------------------------------------------------------
// account-balance manual bridge secret 匹配臂
// ---------------------------------------------------------------------------

func TestW9HManualBridgeSecretArms(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123"
	if matchesAccountBalanceManualSecret(nil, secret) {
		t.Fatal("nil request must not match")
	}
	make := func(authorization string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil)
		request.Header.Set("Authorization", authorization)
		return request
	}
	if matchesAccountBalanceManualSecret(make("Bearer short"), secret) {
		t.Fatal("short token must not match")
	}
	if matchesAccountBalanceManualSecret(make("Basic "+secret), secret) {
		t.Fatal("non-bearer scheme must not match")
	}
	if !matchesAccountBalanceManualSecret(make("Bearer "+secret), secret) {
		t.Fatal("matching bearer token must pass")
	}
	if matchesAccountBalanceManualSecret(make("Bearer wrong-token-wrong-token-wrong-token"), secret) {
		t.Fatal("wrong token must not pass")
	}
}

// ---------------------------------------------------------------------------
// misc helpers
// ---------------------------------------------------------------------------

func TestW9HMiscHelperArms(t *testing.T) {
	if ownermode.Active != "active" {
		t.Fatal("ownermode constant sanity")
	}
	if getenvFrom(map[string]string{"JUHE_AI_SECRET": "s"})("JUHE_AI_SECRET") != "s" {
		t.Fatal("getenvFrom passthrough")
	}
	if getenvFrom(map[string]string{})("JUHE_AI_ABSENT") != "" {
		t.Fatal("absent key reads empty")
	}
	var db *sql.DB
	if db != nil {
		t.Fatal("nil db")
	}
	_ = accountbalance.TriggerManual
}
