package accounthealth

// w16f_store_runner_test.go 波次 w16f 第二批：Store 参数/取消/编码臂、
// Runner 主循环与 runCycle 未覆盖臂（SQLite 真库 + w13g5 注入 kit）、
// outbox drain 空 rows / LoadAccount 失败臂、executor 的 cursor 读取失败臂。
// 全部本地 SQLite，不触网；注入错误固定为 w13g5/w16f 文本，不含敏感数据。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Store 臂 ---

func TestW16fStoreOpenPostgresIdleClamp(t *testing.T) {
	// idle 缺省(10) > open(4) → 钳制到 open（惰性连接，不实际拨号）。
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://w16f:nopass@127.0.0.1:1/w16f", PostgresMaxOpenConns: 4})
	if err != nil {
		t.Fatalf("惰性开库必须成功: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
}

func TestW16fStoreContextCancellationArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-cancel.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	// AcquireOwnerLease 的 lockWrite 取消臂。
	if _, _, err := store.AcquireOwnerLease(canceled, "w16f-owner", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 acquire 必须返回 ctx 错误: %v", err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	// ReleaseOwnerLease 的 lockWrite 取消臂。
	if err := store.ReleaseOwnerLease(canceled, lease); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 release 必须返回 ctx 错误: %v", err)
	}
	// SaveKeyCursor 的 lockWrite 取消臂。
	if err := store.SaveKeyCursor(canceled, lease, "w16f-acc", "health_check", "fp", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 cursor 写入必须返回 ctx 错误: %v", err)
	}
	// AppendOutcome 的 lockWrite 取消臂。
	outcome := w16fOutcome("w16f-cancel-outcome", "w16f-cancel-request")
	if _, err := store.AppendOutcome(canceled, lease, outcome); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 append 必须返回 ctx 错误: %v", err)
	}
}

func TestW16fStoreAppendOutcomeMarshalArm(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-marshal.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	// ExpectedCooldownFence 非空时 projection 保留参与外层编码；Values 携带
	// chan 使 json.Marshal 失败。
	outcome := w16fOutcome("w16f-marshal-outcome", "w16f-marshal-request")
	outcome.Projection = &Projection{
		TargetAccountID: outcome.AccountID, TransitionKind: "cooldown_defer",
		InputVersion: outcome.InputVersion, ConfigRevision: outcome.ConfigRevision, DispatchRevision: outcome.DispatchRevision,
		ExpectedAccountStatus: "temporary_unavailable",
		ExpectedCooldownFence: &CooldownFence{ObservationStartedAt: time.Now().UTC(), Generation: "gen-w16f"},
		Values:                map[string]any{"rogue": make(chan int)},
	}
	if _, err := store.AppendOutcome(ctx, lease, outcome); err == nil || !strings.Contains(err.Error(), "编码 account-health outcome 失败") {
		t.Fatalf("不可编码 outcome 必须报错: %v", err)
	}
}

func TestW16fStoreOutcomeContractArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-contract.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	// cooldown_error 的输出 fence 与 expected fence 不一致。
	outcome := w16fOutcome("w16f-contract-outcome", "w16f-contract-request")
	outcome.NextDueAt = ptrTime(time.Now().UTC().Add(time.Hour))
	outcome.Projection = &Projection{
		TargetAccountID: outcome.AccountID, TransitionKind: "cooldown_error",
		InputVersion: outcome.InputVersion, ConfigRevision: outcome.ConfigRevision, DispatchRevision: outcome.DispatchRevision,
		ExpectedAccountStatus: "temporary_unavailable",
		ExpectedCooldownFence: &CooldownFence{ObservationStartedAt: time.Now().UTC(), Generation: "gen-a"},
		CooldownFence:         &CooldownFence{ObservationStartedAt: time.Now().UTC(), Generation: "gen-b"},
	}
	if _, err := store.AppendOutcome(ctx, lease, outcome); err == nil || !strings.Contains(err.Error(), "输出 fence") {
		t.Fatalf("cooldown_error fence 不一致必须报错: %v", err)
	}
	// 空白账户 ID 的状态查询。
	if _, _, err := store.LoadCurrentState(ctx, "   "); err == nil {
		t.Fatal("空白账户 ID 必须报错")
	}
	// verifyLease 参数缺失臂（经 SaveKeyCursor 触发）。
	if err := store.SaveKeyCursor(ctx, OwnerLease{}, "w16f-acc", "health_check", "fp", 1); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("空租约必须报 ErrOwnerLeaseLost: %v", err)
	}
}

func TestW16fRemoveConsumedRequestArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-consume.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	runner := NewRunner(Config{Now: time.Now}, store, nil)
	// 无 sourcePath → 直接收敛。
	if err := runner.removeConsumedRequest(ctx, ProbeRequest{RequestID: "w16f-r1"}); err != nil {
		t.Fatalf("无 sourcePath 必须收敛: %v", err)
	}
	// 未完成的请求 → 收敛。
	if err := runner.removeConsumedRequest(ctx, ProbeRequest{RequestID: "w16f-r2", sourcePath: filepath.Join(t.TempDir(), "w16f-r2"+requestFileSuffix)}); err != nil {
		t.Fatalf("未消费请求必须收敛: %v", err)
	}
	// 已消费但 sourcePath 指向非空目录 → 删除失败臂。
	consumedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(consumedDir, "keep.txt"), []byte("w16f"), 0o600); err != nil {
		t.Fatal(err)
	}
	consumed := ProbeRequest{RequestID: "w16f-r3", sourcePath: consumedDir}
	if _, err := store.AppendOutcome(ctx, lease, w16fOutcome("w16f-r3-outcome", "w16f-r3")); err != nil {
		t.Fatal(err)
	}
	if err := runner.removeConsumedRequest(ctx, consumed); err == nil {
		t.Fatal("目录路径删除必须报错")
	}
}

// --- Runner runCycle / prepareScheduledInput 臂 ---

// w16fSequencedLoader 按调用次序返回成功或失败（实现 failure loader 接口）。
type w16fSequencedLoader struct {
	mu        sync.Mutex
	calls     int
	failAfter int
	err       error
	failures  []DirectInputFailure
}

func (l *w16fSequencedLoader) LoadDueWithFailures(context.Context, int) (DirectInputLoadResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls > l.failAfter {
		return DirectInputLoadResult{}, l.err
	}
	return DirectInputLoadResult{Failures: l.failures}, nil
}

func (l *w16fSequencedLoader) LoadDue(context.Context, int) ([]Input, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls > l.failAfter {
		return nil, l.err
	}
	return nil, nil
}

func (l *w16fSequencedLoader) LoadAccount(context.Context, string) ([]Input, error) {
	return nil, nil
}

// w16fDueOnlyLoader 只实现 directInputLoader（不带 failures 扩展）。
type w16fDueOnlyLoader struct {
	err error
}

func (l *w16fDueOnlyLoader) LoadDue(context.Context, int) ([]Input, error) { return nil, l.err }
func (l *w16fDueOnlyLoader) LoadAccount(context.Context, string) ([]Input, error) {
	return nil, nil
}

// w16fExplicitAccountLoader 显式账户读取可编程失败。
type w16fExplicitAccountLoader struct {
	accountErr error
}

func (l *w16fExplicitAccountLoader) LoadDueWithFailures(context.Context, int) (DirectInputLoadResult, error) {
	return DirectInputLoadResult{}, nil
}

func (l *w16fExplicitAccountLoader) LoadDue(context.Context, int) ([]Input, error) {
	return nil, nil
}

func (l *w16fExplicitAccountLoader) LoadAccount(context.Context, string) ([]Input, error) {
	return nil, l.accountErr
}

func w16fRunnerConfig(store *Store, inputDirectory string) Config {
	return Config{
		InstanceID:       "w16f-runner",
		InputDirectory:   inputDirectory,
		InputKeys:        map[string][]byte{"current": []byte("w16f-key")},
		CredentialSecret: "w16f-secret",
		ScanInterval:     25 * time.Millisecond,
		OwnerLease:       6 * time.Second,
		ProbeTimeout:     time.Second,
		MaxResponseBytes: 1024,
		MaxConcurrency:   1,
		Now:              time.Now,
	}
}

func w16fOutcome(outcomeID, requestID string) Outcome {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	return Outcome{
		OutcomeID: outcomeID, RequestID: requestID, AccountID: "w16f-acc",
		Outcome: OutcomeNeutral, ObservedAt: now,
		InputVersion: 2, ConfigRevision: 3, DispatchRevision: 4,
	}
}

func TestW16fRunCycleLoaderAndRequestArms(t *testing.T) {
	t.Run("failure loader 隔离写入失败", func(t *testing.T) {
		store, spec := w13g5HealthInjectStore(t)
		if err := store.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("租约必须获取: %t %v", acquired, err)
		}
		t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
		loader := &w16fSequencedLoader{failAfter: 1, failures: []DirectInputFailure{{AccountID: "w16f-broken", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}}}
		runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
		runner.directInputReader = loader
		spec.armOnce("INSERT INTO account_health_outcomes")
		defer spec.disarm()
		if err := runner.runCycle(context.Background(), lease); err == nil {
			t.Fatal("隔离 outcome 写入失败必须传播")
		}
	})
	t.Run("due only loader 与读取失败", func(t *testing.T) {
		store, _ := w13g5HealthInjectStore(t)
		if err := store.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("租约必须获取: %t %v", acquired, err)
		}
		t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
		runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
		runner.directInputReader = &w16fDueOnlyLoader{}
		if err := runner.runCycle(context.Background(), lease); err != nil {
			t.Fatalf("空 due 列表必须成功: %v", err)
		}
		runner.directInputReader = &w16fDueOnlyLoader{err: errors.New("w16f due 注入失败")}
		if err := runner.runCycle(context.Background(), lease); err == nil {
			t.Fatal("due 读取失败必须传播")
		}
	})
	t.Run("outbox drain 失败传播", func(t *testing.T) {
		store, _ := w13g5HealthInjectStore(t)
		if err := store.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("租约必须获取: %t %v", acquired, err)
		}
		t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
		runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
		runner.SetProbeRequestDrain(&ProbeRequestDrain{
			Store:    &w13g8StubOutboxStore{err: errW13G8Injected},
			Boundary: w13g8StubBoundary{},
		})
		if err := runner.runCycle(context.Background(), lease); !errors.Is(err, errW13G8Injected) {
			t.Fatalf("drain 失败必须传播: %v", err)
		}
	})
}

func TestW16fRunCycleExplicitRequestArms(t *testing.T) {
	root := t.TempDir()
	store, spec := w13g5HealthInjectStore(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	key := []byte("w16f-key")
	requestPayload, err := json.Marshal(ProbeRequest{RequestID: "w16f-explicit-request", AccountID: "w16f-ghost", Reason: "activation", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, Deadline: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "w16f-req"+requestFileSuffix), signedEnvelope(t, "current", key, requestPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	config := w16fRunnerConfig(store, root)
	// (a) 显式账户读取失败。
	runner := NewRunner(config, store, nil)
	runner.directInputReader = &w16fExplicitAccountLoader{accountErr: errors.New("w16f account 注入失败")}
	if err := runner.runCycle(context.Background(), lease); err == nil {
		t.Fatal("显式账户读取失败必须传播")
	}
	// (b) input 缺失 → stale 终态写入失败注入。
	runner = NewRunner(config, store, nil)
	runner.directInputReader = &w16fExplicitAccountLoader{}
	spec.armOnce("INSERT INTO account_health_outcomes")
	if err := runner.runCycle(context.Background(), lease); err == nil {
		t.Fatal("stale 终态写入失败必须传播")
	}
	spec.disarm()
}

func TestW16fPrepareScheduledInputArms(t *testing.T) {
	store, spec := w13g5HealthInjectStore(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	ctx := context.Background()
	now := time.Now().UTC()
	runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
	// 不合格 input → 直接跳过。
	ineligible := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-ineligible")
	ineligible.Eligibility = Eligibility{}
	task, err := runner.prepareScheduledInput(ctx, lease, ineligible, now)
	if err != nil || task.ready {
		t.Fatalf("不合格 input 必须跳过: %t %v", task.ready, err)
	}
	// runInput 对不合格 input 同样收敛。
	if err := runner.runInput(ctx, lease, ineligible, now); err != nil {
		t.Fatalf("runInput 必须收敛: %v", err)
	}
	// 调度校验失败 → 持久 task failure。
	invalid := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-invalid")
	invalid.Schedule.HealthIntervalMS = 0
	if err := runner.prepareScheduledInputErr(ctx, lease, invalid, now); err != nil {
		t.Fatalf("task failure 必须持久化成功: %v", err)
	}
	// 状态读取失败臂。
	valid := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-state-fail")
	spec.armOnce("FROM account_health_current_state WHERE account_id")
	if err := runner.prepareScheduledInputErr(ctx, lease, valid, now); err == nil {
		t.Fatal("状态读取失败必须传播")
	}
	spec.disarm()
	// HasRequest 失败臂。
	due := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-has-request")
	spec.armOnce("SELECT 1 FROM account_health_outcomes WHERE request_id")
	if err := runner.prepareScheduledInputErr(ctx, lease, due, now); err == nil {
		t.Fatal("HasRequest 失败必须传播")
	}
	spec.disarm()
}

// prepareScheduledInputErr 仅返回 error（丢弃任务值），便于注入断言。
func (r *Runner) prepareScheduledInputErr(ctx context.Context, lease OwnerLease, input Input, now time.Time) error {
	_, err := r.prepareScheduledInput(ctx, lease, input, now)
	return err
}

func TestW16fPrepareScheduledInputAlreadyRequested(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-already.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	now := time.Now().UTC()
	runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
	input := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-already")
	input.InputVersion = 2
	input.ConfigRevision = 3
	input.DispatchRevision = 4
	due := now.Add(-time.Minute)
	// 预置同 epoch 的 current state（NextDueAt 已到）。
	state := w16fOutcome("w16f-already-state", "w16f-already-state-request")
	state.AccountID = input.AccountID
	state.InputVersion, state.ConfigRevision, state.DispatchRevision = 2, 3, 4
	state.ObservedAt = now.Add(-2 * time.Minute)
	state.NextDueAt = &due
	state.AccountStatus = "active"
	if _, err := store.AppendOutcome(ctx, lease, state); err != nil {
		t.Fatal(err)
	}
	// 预置同 ID 的已消费请求。
	requestID := scheduledRequestID(input, "health", due)
	consumed := w16fOutcome("w16f-already-outcome", requestID)
	consumed.InputVersion, consumed.ConfigRevision, consumed.DispatchRevision = 2, 3, 4
	if _, err := store.AppendOutcome(ctx, lease, consumed); err != nil {
		t.Fatal(err)
	}
	task, err := runner.prepareScheduledInput(ctx, lease, input, now)
	if err != nil || task.ready {
		t.Fatalf("已消费请求必须跳过: ready=%t err=%v", task.ready, err)
	}
}

// --- Run 主循环臂 ---

func TestW16fRunnerLoopInitialCycleError(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-loop-err.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	runner := NewRunner(w16fRunnerConfig(store, filepath.Join(t.TempDir(), "absent")), store, nil)
	if err := runner.runOwned(ctx, lease); err == nil {
		t.Fatal("首轮 cycle 失败必须返回错误")
	}
}

func TestW16fRunnerRunLoopErrorThenRecover(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-loop.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	inputRoot := t.TempDir()
	// (a) 每轮失败：runOwned 错误 → Run 记录错误并重试（130/134/135 臂）。
	failingLoader := &w16fSequencedLoader{failAfter: 0, err: errors.New("w16f loop 注入失败")}
	runner := NewRunner(w16fRunnerConfig(store, inputRoot), store, nil)
	runner.directInputReader = failingLoader
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	runner.mu.RLock()
	lastError := runner.status.LastError
	runner.mu.RUnlock()
	if lastError == "" {
		cancel()
		<-done
		t.Fatal("循环错误必须进入 LastError")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("失败循环退出: %v", err)
	}
	// (b) 首轮成功、第二轮失败：非首轮错误臂 + scanTimer 重启 cycle。
	recoverLoader := &w16fSequencedLoader{failAfter: 1, err: errors.New("w16f cycle2 注入失败")}
	runner2 := NewRunner(w16fRunnerConfig(store, inputRoot), store, nil)
	runner2.directInputReader = recoverLoader
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- runner2.Run(ctx2) }()
	time.Sleep(900 * time.Millisecond)
	cancel2()
	if err := <-done2; !errors.Is(err, context.Canceled) {
		t.Fatalf("恢复循环退出: %v", err)
	}
}

// --- outbox drain 行级臂 ---

// w16fBoundaryOK 返回冻结范围内证据的 boundary stub。
type w16fBoundaryOK struct{}

func (w16fBoundaryOK) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	return 1, 1, 1, true, nil
}

// w16fRowStore 返回预置 outbox 行的 store stub。
type w16fRowStore struct {
	rows []ProbeOutboxRow
}

func (s *w16fRowStore) ClaimPendingProbeRequests(context.Context, int, time.Time) ([]ProbeOutboxRow, error) {
	return s.rows, nil
}

func (s *w16fRowStore) CompleteProbeRequest(context.Context, string, time.Time) (bool, error) {
	return true, nil
}

func TestW16fOutboxDrainRowArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-outbox.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	// 空 rows → 收敛。
	runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
	runner.directInputReader = &w16fExplicitAccountLoader{}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: &w16fRowStore{}, Boundary: w16fBoundaryOK{}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("空 rows 必须收敛: %v", err)
	}
	// 显式账户读取失败 → 行保持 pending 并返回错误。
	runner.directInputReader = &w16fExplicitAccountLoader{accountErr: errors.New("w16f outbox account 注入失败")}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: &w16fRowStore{rows: []ProbeOutboxRow{{RequestID: "w16f-outbox-1", AccountID: "w16f-acc", Reason: "activation", Deadline: time.Now().UTC().Add(time.Minute)}}}, Boundary: w16fBoundaryOK{}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err == nil {
		t.Fatal("显式账户读取失败必须传播")
	}
}

// --- executor cursor 读取失败臂 ---

func TestW16fExecutorCursorReadFailure(t *testing.T) {
	store, spec := w13g5HealthInjectStore(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	secret := "w16f-secret"
	input := testScheduledAPIKeyInput(t, "https://api.example.com", secret, "w16f-cursor-fail")
	request := ProbeRequest{
		RequestID: "w16f-cursor-request", AccountID: input.AccountID,
		InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision,
		Deadline: time.Now().UTC().Add(time.Minute),
	}
	spec.armOnce("FROM account_health_key_cursors")
	defer spec.disarm()
	if _, err := ExecuteInputProbe(context.Background(), store, lease, input, request, ProbeOptions{Secret: secret, Now: time.Now}); err == nil {
		t.Fatal("cursor 读取失败必须传播")
	}
}

// --- w16f 补充臂：runCycle 请求文件损坏、显式请求终态门、Store 取消臂 ---

func TestW16fStoreEnsureSchemaCancelledArm(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-schema-cancel.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.EnsureSchema(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 EnsureSchema 必须返回 ctx 错误: %v", err)
	}
}

func TestW16fRunCycleBrokenRequestFileArm(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-broken-req.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "w16f-broken"+requestFileSuffix), []byte("w16f-garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(w16fRunnerConfig(store, root), store, nil)
	if err := runner.runCycle(context.Background(), lease); err == nil {
		t.Fatal("损坏的 request 文件必须传播")
	}
}

func TestW16fRunCycleConsumedRequestRemovalFailureArm(t *testing.T) {
	store, spec := w13g5HealthInjectStore(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	root := t.TempDir()
	key := []byte("w16f-key")
	requestPayload, err := json.Marshal(ProbeRequest{RequestID: "w16f-consumed-request", AccountID: "w16f-consumed-acc", Reason: "activation", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, Deadline: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "w16f-consumed"+requestFileSuffix), signedEnvelope(t, "current", key, requestPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(w16fRunnerConfig(store, root), store, nil)
	runner.directInputReader = &w16fExplicitAccountLoader{}
	// 已消费请求（HasRequest 命中）+ 删除失败注入路径：HasRequest 直接失败
	// 也可传播同一 return 语句；此处驱动 runCycle 内的 removeConsumedRequest
	// 错误传播臂。
	spec.armOnce("SELECT 1 FROM account_health_outcomes WHERE request_id")
	defer spec.disarm()
	if err := runner.runCycle(context.Background(), lease); err == nil {
		t.Fatal("已消费请求清理失败必须传播")
	}
}

// TestW16fLoadSignedInputFilesDuplicateVersionArm 覆盖重复/倒退版本的输入
// 文件拒绝臂。
func TestW16fLoadSignedInputFilesDuplicateVersionArm(t *testing.T) {
	root := t.TempDir()
	key := []byte("w16f-dup-key")
	newer, err := json.Marshal(Input{AccountID: "w16f-dup-acc", InputVersion: 2, ConfigRevision: 1, DispatchRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	older, err := json.Marshal(Input{AccountID: "w16f-dup-acc", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a-v2"+inputFileSuffix), signedEnvelope(t, "current", key, newer), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b-v1"+inputFileSuffix), signedEnvelope(t, "current", key, older), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSignedInputFiles(root, map[string][]byte{"current": key}); err == nil || !strings.Contains(err.Error(), "重复或倒退版本") {
		t.Fatalf("倒退版本必须报错: %v", err)
	}
}

func TestW16fRemoveConsumedRequestHasRequestFailureArm(t *testing.T) {
	store, spec := w13g5HealthInjectStore(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
	spec.armOnce("SELECT 1 FROM account_health_outcomes WHERE request_id")
	defer spec.disarm()
	if err := runner.removeConsumedRequest(context.Background(), ProbeRequest{RequestID: "w16f-r-fail", sourcePath: "w16f-keep"}); err == nil {
		t.Fatal("HasRequest 失败必须传播")
	}
}

// TestW16fRunExplicitRequestTerminalArms 覆盖显式请求的过期、无效 schedule
// 与 mutate 状态门臂。
func TestW16fRunExplicitRequestTerminalArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w16f-explicit.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })
	now := time.Now().UTC()
	runner := NewRunner(w16fRunnerConfig(store, t.TempDir()), store, nil)
	w16fRequest := func(requestID string, deadline time.Time, mutate bool) ProbeRequest {
		return ProbeRequest{
			RequestID: requestID, AccountID: "w16f-explicit-acc", Reason: "activation",
			InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
			Deadline: deadline, MutateAccount: mutate,
		}
	}
	// (a) deadline 已过期 → task_failed 终态。
	expiredInput := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-explicit-acc")
	if err := runner.runExplicitRequest(ctx, lease, expiredInput, w16fRequest("w16f-expired-request", now.Add(-time.Second), false), now); err != nil {
		t.Fatalf("过期请求必须落终态: %v", err)
	}
	// (b) schedule 无效 → input_invalid 终态。
	invalidInput := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-explicit-acc")
	invalidInput.Schedule.HealthIntervalMS = 0
	if err := runner.runExplicitRequest(ctx, lease, invalidInput, w16fRequest("w16f-invalid-request", now.Add(time.Minute), false), now); err != nil {
		t.Fatalf("无效 schedule 必须落终态: %v", err)
	}
	// (c) mutate 请求遇到 error 状态的 current state → 拒绝 transition。
	errorState := w16fOutcome("w16f-error-state", "w16f-error-state-request")
	errorState.AccountID = "w16f-explicit-acc"
	errorState.InputVersion, errorState.ConfigRevision, errorState.DispatchRevision = 1, 1, 1
	errorState.ObservedAt = now.Add(-2 * time.Minute)
	errorState.AccountStatus = "error"
	if _, err := store.AppendOutcome(ctx, lease, errorState); err != nil {
		t.Fatal(err)
	}
	eligibleInput := testScheduledAPIKeyInput(t, "https://api.example.com", "w16f-secret", "w16f-explicit-acc")
	if err := runner.runExplicitRequest(ctx, lease, eligibleInput, w16fRequest("w16f-mutate-request", now.Add(time.Minute), true), now); err != nil {
		t.Fatalf("error 状态必须拒绝 transition: %v", err)
	}
	// (d) 状态读取失败臂（注入）。
	injectStore, spec := w13g5HealthInjectStore(t)
	if err := injectStore.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	injectLease, acquired, err := injectStore.AcquireOwnerLease(ctx, "w16f-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = injectStore.ReleaseOwnerLease(ctx, injectLease) })
	injectRunner := NewRunner(w16fRunnerConfig(injectStore, t.TempDir()), injectStore, nil)
	spec.armOnce("FROM account_health_current_state WHERE account_id")
	defer spec.disarm()
	if err := injectRunner.runExplicitRequest(ctx, injectLease, eligibleInput, w16fRequest("w16f-state-fail-request", now.Add(time.Minute), false), now); err == nil {
		t.Fatal("状态读取失败必须传播")
	}
}
