package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

// wgNewProbeStoreFixture 构造探针族存储（业务库 + proberepo.Store），
// 供电路恢复解析器与探针闭包直测。
func wgNewProbeStoreFixture(t *testing.T) *proberepo.Store {
	t.Helper()
	db, _ := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(probeCoreTablesPGDDL)
		return err
	})
	store, err := proberepo.NewStore(proberepo.Config{DB: db, Postgres: true, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestCircuitRecoveryResolverBranches 覆盖电路恢复目标解析的全部否定分支
// （组合根适配器，正路径由调度冒烟的空 Sweep 覆盖）。
func TestCircuitRecoveryResolverBranches(t *testing.T) {
	store := wgNewProbeStoreFixture(t)
	resolver := circuitRecoveryTargetResolver{store: store}
	ctx := context.Background()

	// ctx 已取消 → 错误透传。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := resolver.Resolve(canceled, opsjobs.CircuitState{}); err == nil {
		t.Fatal("取消的 ctx 必须报错")
	}
	// 运行态键无法解析身份 → (false, nil)。
	target, ok, err := resolver.Resolve(ctx, opsjobs.CircuitState{Scope: opsjobs.CircuitScope{AccountRuntimeKey: "::bad-key"}})
	if err != nil || ok || target.Probe != nil {
		t.Fatalf("非法运行态键必须返回 false: %v %v %v", target, ok, err)
	}
	// owner 身份但账户缺失 → false。
	target, ok, err = resolver.Resolve(ctx, opsjobs.CircuitState{Scope: opsjobs.CircuitScope{AccountRuntimeKey: "gateway:account:wg-missing"}})
	if err != nil || ok {
		t.Fatalf("缺失账户必须返回 false: %v %v %v", target, ok, err)
	}
}

// TestCircuitRecoveryTransportProbeTaskFailure 覆盖探针任务失败分支
// （observation 为空或探针报错 → unknown/task_failure，不计入失败证据）。
func TestCircuitRecoveryTransportProbeTaskFailure(t *testing.T) {
	store := wgNewProbeStoreFixture(t)
	service, err := accountprobe.NewService(accountprobe.Options{Source: store, Secret: wgBalanceSecret, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	identity := opsjobs.RecoveryRuntimeIdentity{Kind: "owner", AccountID: "wg-missing"}
	// 零值 scope 无 modelBucket → 不钉住，回退健康检查模型（既有行为）。
	req := circuitRecoveryProbeRequest(identity, opsjobs.CircuitState{}, "g1", "sys1")
	outcome := circuitRecoveryTransportProbe(context.Background(), service, req)
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("任务失败必须分类为 unknown/task_failure: %+v", outcome)
	}
}

// TestSpeedFirstProbeFuncTaskFailure 覆盖速度优先探针闭包的任务失败分支。
func TestSpeedFirstProbeFuncTaskFailure(t *testing.T) {
	store := wgNewProbeStoreFixture(t)
	service, err := accountprobe.NewService(accountprobe.Options{Source: store, Secret: wgBalanceSecret, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	probe := speedFirstProbeFunc(service)
	snapshot, outcome := probe(context.Background(), nil, opsjobs.ProbeCandidate{
		AccountID: "wg-missing",
	}, nil)
	if snapshot.Success {
		t.Fatalf("任务失败不得报告成功: %+v", snapshot)
	}
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("任务失败必须分类为 unknown/task_failure: %+v", outcome)
	}
}

// TestSlogWriterLoggerAndEnqueuer 覆盖 usagewriter 日志适配器与
// retention 队列投递适配器。
func TestSlogWriterLoggerAndEnqueuer(t *testing.T) {
	// slogWriterLogger（usagewriter 适配器）两个级别不 panic 即可。
	logger := slogWriterLogger{logger: assemblyTestLogger()}
	logger.Warn("wg_warn", map[string]any{"k": "v"})
	logger.Error("wg_error", map[string]any{"k": "v"})

	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 64 * 1024})
	enqueuer := &queueEnqueuer{queue: queue}
	if result := enqueuer.Enqueue(context.Background(), wgMaintenanceJob("api_key_related_cleanup", "x")); !result.Queued {
		t.Fatalf("正常投递必须成功: %+v", result)
	}
	if err := enqueuer.EnqueueAsync(context.Background(), wgMaintenanceJob("api_key_related_cleanup", "y")); err != nil {
		t.Fatalf("异步投递必须成功: %v", err)
	}
	// 队列满 → 异步投递显式报错。
	tiny := newRecordMaintenanceQueue(queueLimits{MaxItems: 0, MaxBytes: 4})
	if err := (&queueEnqueuer{queue: tiny}).EnqueueAsync(context.Background(), wgMaintenanceJob("api_key_related_cleanup", "z")); err == nil {
		t.Fatal("超限异步投递必须报错")
	}
}
