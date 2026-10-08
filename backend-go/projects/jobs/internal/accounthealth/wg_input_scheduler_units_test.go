package accounthealth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadConfigErrorMatrix 表驱动覆盖 LoadConfig 的 fail closed 分支。
func TestLoadConfigErrorMatrix(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "wg",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     "/tmp/wg/j1.sqlite3",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   "/tmp/wg/inputs",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "secret",
		}
	}
	cases := []struct {
		name   string
		mutate func(env map[string]string)
	}{
		{"缺 owner 声明", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = "node" }},
		{"store 模式非法", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_STORE"] = "oracle" }},
		{"PG 缺 URL", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_STORE"] = "postgres" }},
		{"input 源非法", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "redis" }},
		{"签名键过短", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = "AAAA" }},
		// 「缺凭据 secret」非生产臂已随 2026-09-19 零配置决策回退开发密钥；
		// production 下缺凭据 secret 仍 fail closed。
		{"production 缺凭据 secret", func(env map[string]string) {
			env["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = ""
			env["NODE_ENV"] = "production"
		}},
		// 「sqlite 缺路径」「缺 input 目录」「签名键缺失」三个失败臂已删除
		// （2026-09-19 零配置决策：三项均改为派生缺省，不再 fail closed）。
		{"owner lease 小于探针超时", func(env map[string]string) {
			env["JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE"] = "16s"
			env["JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT"] = "30s"
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			env := base()
			item.mutate(env)
			if _, err := LoadConfig(getenvFrom(env)); err == nil {
				t.Fatal("非法配置必须 fail closed")
			}
		})
	}
	// 合法基线必须通过。
	if _, err := LoadConfig(getenvFrom(base())); err != nil {
		t.Fatalf("基线配置必须通过: %v", err)
	}
}

// TestRunnerRunLoopBranches 覆盖 Run 循环的租约获取失败与竞争等待分支。
func TestRunnerRunLoopBranches(t *testing.T) {
	store := wgNewStore(t)
	// 场景一：租约被他人持有 → 竞争等待路径。
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "other-owner", 10*time.Minute)
	if err != nil || !acquired {
		t.Fatalf("预占租约: %v %v", acquired, err)
	}
	runner := NewRunner(Config{InstanceID: "wg-loop", ScanInterval: 20 * time.Millisecond, OwnerLease: time.Minute}, store, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("竞争等待必须可被 ctx 收口: %v", err)
	}
	if runner.Status().LastError != "" {
		t.Fatalf("竞争等待不算错误: %q", runner.Status().LastError)
	}
	_ = store.ReleaseOwnerLease(context.Background(), lease)

	// 场景二：store 句柄已关闭 → 获取失败 → setError 后由 ctx 收口。
	broken, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "broken.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	brokenRunner := NewRunner(Config{InstanceID: "wg-broken", ScanInterval: 20 * time.Millisecond, OwnerLease: time.Minute}, broken, nil)
	brokenCtx, brokenCancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer brokenCancel()
	if err := brokenRunner.Run(brokenCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("获取失败循环必须可被 ctx 收口: %v", err)
	}
	if brokenRunner.Status().LastError == "" {
		t.Fatal("获取失败必须记录 LastError")
	}
	if brokenRunner.Ready() {
		t.Fatal("失败状态不得就绪")
	}
}

// TestSetOwnerHeldTransitions 覆盖 owner 状态翻转与 LastScan 更新。
func TestSetOwnerHeldTransitions(t *testing.T) {
	store := wgNewStore(t)
	runner := NewRunner(Config{InstanceID: "wg-owned"}, store, nil)
	runner.setOwnerHeld(true)
	if !runner.Status().OwnerHeld {
		t.Fatal("setOwnerHeld(true) 必须生效")
	}
	runner.setOwnerHeld(false)
	if runner.Status().OwnerHeld {
		t.Fatal("setOwnerHeld(false) 必须生效")
	}
}

// ---- outbox 消费面 fake 端口（脚本化、可回放） ----

type wgFakeOutboxStore struct {
	rows      []ProbeOutboxRow
	completed []string
	claimErr  error
}

func (s *wgFakeOutboxStore) ClaimPendingProbeRequests(_ context.Context, limit int, _ time.Time) ([]ProbeOutboxRow, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if limit > len(s.rows) {
		limit = len(s.rows)
	}
	return s.rows[:limit], nil
}

func (s *wgFakeOutboxStore) CompleteProbeRequest(_ context.Context, requestID string, _ time.Time) (bool, error) {
	s.completed = append(s.completed, requestID)
	return true, nil
}

type wgFakeBoundary struct {
	revision int64
	ok       bool
	err      error
}

func (b *wgFakeBoundary) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	if b.err != nil {
		return 0, 0, 0, false, b.err
	}
	return b.revision, b.revision, 1, b.ok, nil
}

// TestDrainProbeRequestOutboxSemantics 覆盖 outbox 消费的确定性收敛、
// 瞬态错误与 fence 结算分支。
func TestDrainProbeRequestOutboxSemantics(t *testing.T) {
	store := wgNewStore(t)
	runner := NewRunner(Config{InstanceID: "wg-drain", Now: func() time.Time { return time.Unix(0, 0).UTC() }}, store, nil)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	lease := wgStoreLease(t, store, "wg-drain")

	// 未装配 → 静默跳过。
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("未装配必须静默: %v", err)
	}

	// 字段缺失 → 确定性收敛（返回 nil 并删行）。
	invalidStore := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: " ", AccountID: "acct"}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: invalidStore, Boundary: &wgFakeBoundary{ok: true}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("字段缺失必须收敛: %v", err)
	}
	if len(invalidStore.completed) != 1 {
		t.Fatalf("确定性失败必须删行: %v", invalidStore.completed)
	}

	// 范围外账户 → 结算 fence = unknown。
	settled := SourceFence{StateKey: "s", AccountID: "a"}
	outOfScope := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{
		RequestID: "req-out", AccountID: "acct", Reason: "manual",
		SourceFence: &settled, Deadline: now.Add(time.Minute),
	}}}
	settleCalls := 0
	runner.SetProbeRequestDrain(&ProbeRequestDrain{
		Store: outOfScope, Boundary: &wgFakeBoundary{ok: false},
		SettleFence: func(_ context.Context, fence SourceFence, state string) error {
			settleCalls++
			if state != "unknown" || fence.AccountID != "a" {
				t.Fatalf("结算参数错误: %+v %s", fence, state)
			}
			return nil
		},
	})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("范围外消费必须成功: %v", err)
	}
	if settleCalls != 1 {
		t.Fatalf("fence 必须结算一次: %d", settleCalls)
	}

	// fence 与 config revision 不一致 → 确定性放弃。
	staleFence := settled
	staleFence.ConfigRevision = 99
	staleStore := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{
		RequestID: "req-stale", AccountID: "acct", Reason: "manual",
		SourceFence: &staleFence, Deadline: now.Add(time.Minute),
	}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: staleStore, Boundary: &wgFakeBoundary{ok: true, revision: 1}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("stale fence 必须确定性收敛: %v", err)
	}
	if len(staleStore.completed) != 1 {
		t.Fatal("stale fence 行必须删行")
	}

	// boundary 瞬态错误 → 返回错误、行保持 pending。
	errorStore := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: "req-err", AccountID: "acct", Reason: "manual", Deadline: now.Add(time.Minute)}}}
	boundaryErr := errors.New("业务库读取失败")
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: errorStore, Boundary: &wgFakeBoundary{err: boundaryErr}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); !errors.Is(err, boundaryErr) {
		t.Fatalf("瞬态错误必须透传: %v", err)
	}
	if len(errorStore.completed) != 0 {
		t.Fatal("瞬态错误行不得删行")
	}

	// claim 失败 → 返回错误。
	failingStore := &wgFakeOutboxStore{claimErr: errors.New("claim 失败")}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: failingStore, Boundary: &wgFakeBoundary{ok: true}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err == nil {
		t.Fatal("claim 失败必须透传")
	}

	// 范围内账户 → runExplicitRequest 落 stale 终态（无直读器输入）并删行。
	inScope := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{
		RequestID: "req-in", AccountID: "acct", Reason: "manual", Deadline: now.Add(time.Minute),
	}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: inScope, Boundary: &wgFakeBoundary{ok: true, revision: 7}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("范围内消费必须成功: %v", err)
	}
	if len(inScope.completed) != 1 {
		t.Fatal("范围内行必须删行")
	}
	found, err := store.HasRequest(context.Background(), "req-in")
	if err != nil || !found {
		t.Fatalf("stale 终态必须持久化: %v %v", found, err)
	}
}
