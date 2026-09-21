package main

// w40 覆盖率补口（组合根/接线尾部）：近期提交新增但单测未触达的分支。
//   - main.go owner 租约等待（loadOwnerLeaseAcquireWait / startLeaseKeeperWithWait）
//   - chain_obs_wiring.go 观测适配器的 nil-observer / nil-services 臂与 slog 投影
//   - chain_usage_wiring.go 头投影的宽容值形态
//   - health_route.go validate 的 nil-atomic fail-fast 臂
//   - j3b_memory_keymodel.go RecordFailureIntent 零 ObservedAt 回落臂（零配置自动认领）
//   - storage_bootstrap.go ensureJ3bDedicatedSQLiteBootstrap 打开失败臂
//   - compose_routestrategies_wiring.go facade 的 nil-service 端口臂

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
)

func TestLoadOwnerLeaseAcquireWaitParsing(t *testing.T) {
	t.Setenv(ownerLeaseAcquireWaitEnv, "")
	// nil getenv → os.Getenv 回落（1021 臂）；env 未配置 → 0。
	if wait, err := loadOwnerLeaseAcquireWait(nil); err != nil || wait != 0 {
		t.Fatalf("nil getenv = %v err=%v", wait, err)
	}
	// 空白 → 0。
	if wait, err := loadOwnerLeaseAcquireWait(func(string) string { return "  " }); err != nil || wait != 0 {
		t.Fatalf("空白 = %v err=%v", wait, err)
	}
	// 合法 duration。
	wait, err := loadOwnerLeaseAcquireWait(func(string) string { return "1500ms" })
	if err != nil || wait != 1500*time.Millisecond {
		t.Fatalf("1500ms = %v err=%v", wait, err)
	}
	// 解析失败 → 配置错误（1028-1030 臂）。
	if _, err := loadOwnerLeaseAcquireWait(func(string) string { return "abc" }); err == nil ||
		!strings.Contains(err.Error(), "must be a positive duration") {
		t.Fatalf("非法 duration 必须报错: %v", err)
	}
	// 负值 → 配置错误（1029 臂）。
	if _, err := loadOwnerLeaseAcquireWait(func(string) string { return "-2s" }); err == nil {
		t.Fatal("负值必须报错")
	}
}

func TestStartLeaseKeeperWithWait(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	// wait<=0：被持有时立即返回 ok=false，start 只调用一次。
	calls := 0
	keeper, ok, err := startLeaseKeeperWithWait(discard, "lease", 0, func() (int, bool, error) {
		calls++
		return 7, false, nil
	})
	if ok || err != nil || keeper != 7 || calls != 1 {
		t.Fatalf("wait=0: keeper=%d ok=%v err=%v calls=%d", keeper, ok, err, calls)
	}

	// err 非 nil：立即返回不重试。
	calls = 0
	wantErr := errors.New("w40 transport failure")
	_, ok, err = startLeaseKeeperWithWait(discard, "lease", time.Hour, func() (int, bool, error) {
		calls++
		return 0, false, wantErr
	})
	if !errors.Is(err, wantErr) || ok || calls != 1 {
		t.Fatalf("err 臂: ok=%v err=%v calls=%d", ok, err, calls)
	}

	// 首次即成功：不进等待循环。
	calls = 0
	keeper, ok, err = startLeaseKeeperWithWait(discard, "lease", time.Hour, func() (int, bool, error) {
		calls++
		return 9, true, nil
	})
	if !ok || err != nil || keeper != 9 || calls != 1 {
		t.Fatalf("首次成功: keeper=%d ok=%v err=%v calls=%d", keeper, ok, err, calls)
	}

	// nil logger → slog.Default()（1051 臂）；重试后成功。
	calls = 0
	_, ok, err = startLeaseKeeperWithWait(nil, "lease", 40*time.Millisecond, func() (int, bool, error) {
		calls++
		return 0, calls >= 2, nil
	})
	if !ok || err != nil || calls != 2 {
		t.Fatalf("重试成功: ok=%v err=%v calls=%d", ok, err, calls)
	}

	// deadline 到仍被持有：ok=false（1059 / 1063 臂）。
	calls = 0
	_, ok, err = startLeaseKeeperWithWait(discard, "lease", 30*time.Millisecond, func() (int, bool, error) {
		calls++
		return 0, false, nil
	})
	if ok || err != nil || calls < 2 {
		t.Fatalf("deadline 到期: ok=%v err=%v calls=%d", ok, err, calls)
	}
}

func TestChainSlogObserverLoggerProjectsFields(t *testing.T) {
	logger := chainSlogObserverLogger{inner: slog.New(slog.NewTextHandler(io.Discard, nil))}
	fields := map[string]interface{}{"kind": "circuit_dispatch", "count": 2}
	logger.Info(fields, "info message")
	logger.Warn(fields, "warn message")
	logger.Debug(fields, "debug message")
	converted := convertAnyFields(fields)
	if converted["kind"] != "circuit_dispatch" {
		t.Fatalf("convertAnyFields 丢失字段: %#v", converted)
	}
	if len(convertAnyFields(map[string]interface{}{})) != 0 {
		t.Fatal("空 fields 必须返回空映射")
	}
}

func TestChainObservabilityNilObserverArms(t *testing.T) {
	// nil observer：sink / hot-quality / wall-budget 三个适配器保持 no-op。
	newChainCircuitObservabilitySink(nil)(gatewaycircuit.RoutingObservabilityEvent{})
	chainHotQualityObserver{}.ObserveGatewayRouting(gatewayhotquality.RoutingObservation{})
	chainRoutingWallBudgetObserver{}.ObserveRouting("budget", "precommit_clipped", 0)
	// wireGatewayObservabilityArms 的 nil-services 臂。
	if err := wireGatewayObservabilityArms(nil, runtimeConfig{}); err != nil {
		t.Fatalf("nil services 必须直接成功: %v", err)
	}
}

func TestChainUsageHTTPHeaderOfValueShapes(t *testing.T) {
	header := chainUsageHTTPHeaderOf(map[string]any{
		"x-string":      "s1",
		"x-slice":       []string{"a", "b"},
		"x-any":         []any{"c", 42, "d"},
		"x-unsupported": 7,
	})
	if got := header.Get("X-String"); got != "s1" {
		t.Fatalf("string 形态 = %q", got)
	}
	if got := header.Values("X-Slice"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("[]string 形态 = %#v", got)
	}
	if got := header.Values("X-Any"); len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Fatalf("[]any 形态（非字符串项丢弃） = %#v", got)
	}
	if len(header.Values("X-Unsupported")) != 0 {
		t.Fatal("不支持形态必须丢弃")
	}
	if len(chainUsageHTTPHeaderOf(nil)) != 0 {
		t.Fatal("nil headers 必须返回空 header")
	}
}

func TestGatewayOwnerHealthValidateRejectsNilAtomics(t *testing.T) {
	if err := (&gatewayOwnerHealth{}).validate(); err == nil {
		t.Fatal("nil atomic 必须在组合期 fail-fast")
	}
}

func TestJ3bMemoryKeyModelGateRecordFailureIntentZeroObservedAt(t *testing.T) {
	gate := newJ3bMemoryKeyModelGate()
	capability := keymodelruntime.Capability{
		CredentialSourceAccountID: "acc-w40",
		KeyFingerprint:            "fp-w40",
		ClientModel:               "gpt-5.6",
		ClientEndpointFamily:      "responses",
		FinalUpstreamModel:        "gpt-5.6",
		UpstreamEndpointMode:      "responses",
		DispatchRevision:          1,
	}
	// 零值 ObservedAt 回落 time.Now（39 臂）。
	status, _, err := gate.RecordFailureIntent(context.Background(), keymodelruntime.FailureIntent{
		IntentID: "w40-zero", RequestID: "w40-zero", AttemptID: "w40-zero",
		Capability: capability,
	})
	if err != nil {
		t.Fatalf("zero observedAt: %v", err)
	}
	switch status {
	case keymodelruntime.StatusApplied, keymodelruntime.StatusIdempotent, keymodelruntime.StatusNotDue:
	default:
		t.Fatalf("unexpected mutation status: %v", status)
	}
}

func TestEnsureJ3bDedicatedSQLiteBootstrapRejectsBlockedPath(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "block")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	err := ensureJ3bDedicatedSQLiteBootstrap(context.Background(), filepath.Join(blocker, "j3b.sqlite3"))
	if err == nil {
		t.Fatal("父路径是文件时必须打开失败")
	}
	if !strings.Contains(err.Error(), "open J3b dedicated sqlite") {
		t.Fatalf("错误应保留原始打开语义: %v", err)
	}
}

func TestRouteStrategySpeedFirstFacadeNilServiceArms(t *testing.T) {
	facade := routeStrategySpeedFirstFacade{}
	items, available, err := facade.ListDegradedRuntime(context.Background(), nil, []string{"rs_w40"})
	if err != nil || available || items != nil {
		t.Fatalf("nil service list: items=%#v available=%v err=%v", items, available, err)
	}
	cleared, err := facade.ClearDegradedRuntime(context.Background(), "rs_w40")
	if err != nil || cleared != 0 {
		t.Fatalf("nil service clear: cleared=%d err=%v", cleared, err)
	}
}
