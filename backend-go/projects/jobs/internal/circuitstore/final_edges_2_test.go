package circuitstore

// 收尾二：用 Eval stub 覆盖 Redis 返回值的错误出口（返回值无效/解析失败）、
// PG 方言的纯 SQL 构造分支与 runtime dependency 状态机的剩余分支。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// evalStubClient 覆写 Eval 返回预设结果；其余命令透传到内层 miniredis
// client（ReplaceAccountDispatchRevision 等多步方法需要 HLen/HGet 正常）。
type evalStubClient struct {
	redis.Cmdable
	inner  redis.Cmdable
	result *redis.Cmd
}

func (e *evalStubClient) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	return e.result
}

func (e *evalStubClient) HLen(ctx context.Context, key string) *redis.IntCmd {
	return e.inner.HLen(ctx, key)
}

func newEvalStub(t *testing.T, value any, err error) *evalStubClient {
	t.Helper()
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	inner := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = inner.Close() })
	return &evalStubClient{inner: inner, result: redis.NewCmdResult(value, err)}
}

// TestStoreEvalErrorPaths 覆盖 Lua 返回值无效/Redis 错误时的错误出口。
func TestStoreEvalErrorPaths(t *testing.T) {
	scope := Scope{Kind: "account", AccountRuntimeKey: "acc-1"}
	paths := []struct {
		name   string
		stub   *evalStubClient
		action func(s *RedisStore) error
	}{
		{"Eval底层错误", newEvalStub(t, nil, errors.New("conn refused")), func(s *RedisStore) error {
			_, err := s.Get(context.Background(), scope, nil)
			return err
		}},
		{"Get返回非字符串", newEvalStub(t, nil, nil), func(s *RedisStore) error {
			_, err := s.Get(context.Background(), scope, nil)
			return err
		}},
		{"Restore返回空", newEvalStub(t, "", nil), func(s *RedisStore) error {
			_, err := s.Restore(context.Background(), State{ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT"}, nil)
			return err
		}},
		{"Restore返回非字符串", newEvalStub(t, nil, nil), func(s *RedisStore) error {
			_, err := s.Restore(context.Background(), State{ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT"}, nil)
			return err
		}},
		{"Restore返回坏JSON", newEvalStub(t, "{broken", nil), func(s *RedisStore) error {
			_, err := s.Restore(context.Background(), State{ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT"}, nil)
			return err
		}},
		{"Restore返回缺status", newEvalStub(t, `{"state":{"phase":"OPEN"}}`, nil), func(s *RedisStore) error {
			_, err := s.Restore(context.Background(), State{ScopeKey: MustScopeKey(scope), Scope: scope, Phase: "SUSPECT"}, nil)
			return err
		}},
		{"转移返回坏JSON", newEvalStub(t, "{broken", nil), func(s *RedisStore) error {
			_, err := s.AcquireCanaryLease(context.Background(), AcquireLeaseInput{
				Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "tr",
				LeaseID: "l", LeaseUntilMs: 9000, NowMs: &[]int64{1000}[0],
			})
			return err
		}},
		{"转移返回空串", newEvalStub(t, "", nil), func(s *RedisStore) error {
			_, err := s.AcquireCanaryLease(context.Background(), AcquireLeaseInput{
				Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "tr",
				LeaseID: "l", LeaseUntilMs: 9000, NowMs: &[]int64{1000}[0],
			})
			return err
		}},
		{"清除证据Eval错误", newEvalStub(t, nil, errors.New("conn refused")), func(s *RedisStore) error {
			_, err := s.ClearAccountEscalationEvidence(context.Background(), "acc", "5", "e", nil)
			return err
		}},
		{"清除证据返回坏类型", newEvalStub(t, nil, nil), func(s *RedisStore) error {
			_, err := s.ClearAccountEscalationEvidence(context.Background(), "acc", "5", "e", nil)
			return err
		}},
		{"清除证据返回非数字串", newEvalStub(t, "abc", nil), func(s *RedisStore) error {
			_, err := s.ClearAccountEscalationEvidence(context.Background(), "acc", "5", "e", nil)
			return err
		}},
		{"ListDue返回空", newEvalStub(t, "", nil), func(s *RedisStore) error {
			_, err := s.ListDue(context.Background(), 1000, 5)
			return err
		}},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			store := &RedisStore{client: tc.stub, now: func() int64 { return 1000 }, capacity: 4}
			if err := tc.action(store); err == nil {
				t.Fatal("坏返回值必须报错")
			}
		})
	}
	// Restore 的状态归一化失败与 scopeKey 不一致分支（不触达 Redis）。
	stub := newEvalStub(t, "unused", nil)
	store := &RedisStore{client: stub, now: func() int64 { return 1000 }, capacity: 4}
	if _, err := store.Restore(context.Background(), State{
		Scope: scope, Phase: "SUSPECT", ConfirmationFailuresRequired: &[]int64{9}[0],
	}, nil); err == nil {
		t.Fatal("归一化失败必须报错")
	}
	if _, err := store.Restore(context.Background(), State{
		ScopeKey: "wrong", Scope: scope, Phase: "SUSPECT",
	}, nil); err == nil {
		t.Fatal("scopeKey 不一致必须报错")
	}
}

// TestCompleteConfirmationRecoveringDisposition 覆盖 framing recovering 分支。
func TestCompleteConfirmationRecoveringDisposition(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	ctx := context.Background()
	scope := accountScope("acc-recovering")
	suspect := opsjobs.CircuitState{
		ScopeKey: MustScopeKey(convertScope(scope)), Scope: scope,
		Phase: opsjobs.CircuitPhaseSuspect, Generation: 1, DispatchRevision: "5",
		TransitionID: "in-1", IncidentID: "in-1",
		ConfirmationFailuresRequired: &[]int{1}[0], ConfirmationFailureCount: &[]int{0}[0],
		RetryAtMS: &[]int64{now() - 5}[0], UpdatedAtMS: now(),
	}
	if _, err := store.Restore(ctx, suspect, now()); err != nil {
		t.Fatal(err)
	}
	identity := opsjobs.CircuitTransitionIdentity{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "tr-a", NowMS: now()}
	if _, err := store.AcquireConfirmationLease(ctx, identity, opsjobs.CircuitLeaseSpec{LeaseID: "l-r", LeaseUntilMS: now() + 5000}); err != nil {
		t.Fatal(err)
	}
	complete := identity
	complete.TransitionID = "tr-b"
	disposition := "recovering"
	result, err := store.CompleteConfirmation(ctx, complete, "l-r", opsjobs.CircuitCompletion{
		Outcome:                    opsjobs.CircuitVerdictFramingComplete,
		FramingCompleteDisposition: disposition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Phase != opsjobs.CircuitPhaseRecovering {
		t.Fatalf("recovering disposition 应进入 RECOVERING: %s", result.State.Phase)
	}
}

// TestPostgresDialectSQLConstruction 覆盖双模方言的纯构造分支（不触达 DB）。
func TestPostgresDialectSQLConstruction(t *testing.T) {
	pgLoader := &ProjectionItemLoader{postgres: true, statsPostgres: true}
	if got := pgLoader.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("PG 业务表前缀: %s", got)
	}
	if got := pgLoader.statsTable("usage_stats_daily"); got != "juhe_stats.usage_stats_daily" {
		t.Fatalf("PG 统计表前缀: %s", got)
	}
	// managementPageSQL 的 PG LATERAL 分支（构造成功且含 LATERAL）。
	pgSQL := pgLoader.managementPageSQL(true, 2)
	if !strings.Contains(pgSQL, "LEFT JOIN LATERAL") || !strings.Contains(pgSQL, "juhe_business.accounts") {
		t.Fatalf("PG 分页 SQL 应走 LATERAL 分支: %s", pgSQL[:120])
	}
	// SQLite 分支走窗口 CTE。
	sqliteSQL := pgLoader.managementPageSQL(false, 2)
	if !strings.Contains(sqliteSQL, "ranked_group_bindings") {
		t.Fatal("SQLite 分页 SQL 应走窗口 CTE 分支")
	}
	pgRepo := &ControlPlaneRepo{postgres: true}
	if got := pgRepo.table("account_circuit_incidents"); got != "juhe_business.account_circuit_incidents" {
		t.Fatalf("控制面 PG 表前缀: %s", got)
	}
	listRepo := &ListAvailabilityRepo{postgres: true}
	if got := listRepo.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("投影 repo PG 表前缀: %s", got)
	}
	// instantParam 的 PG time.Time 分支。
	parsed, ok := instantParam(true, "2030-01-01T00:00:00.000Z", nil).(time.Time)
	if !ok || parsed.IsZero() {
		t.Fatalf("PG 合法时间应返回 time.Time: %v", parsed)
	}
}

// TestRuntimeDependencyRemainingBranches 覆盖依赖状态机的剩余分支。
func TestRuntimeDependencyRemainingBranches(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	// 无依赖行 → bootstrap：插入 recovering 行并启动（ErrNoRows 分支）。
	started, err := repo.BeginRuntimeDependencyRecovery(ctx, "2026-09-04T10:00:00.000Z")
	if err != nil || !started {
		t.Fatalf("无依赖行应按 bootstrap 启动: %v %v", started, err)
	}
	if err := repo.EnsureRuntimeDependency(ctx, ""); err != nil {
		t.Fatalf("空白 updatedAt 回落当前时钟: %v", err)
	}
	if err := repo.TouchRuntimeDependency(ctx, "2026-09-04T10:00:00.000Z"); err != nil {
		t.Fatalf("healthy 状态 touch: %v", err)
	}
	// 全量重放会把 viewer health 置为非 current。
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'viewer-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnqueueAllForRuntimeRecovery(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	// dirty 存在 → recovery 不得完成。
	completed, err := repo.CompleteRuntimeDependencyRecovery(ctx, "2026-09-04T10:00:00.000Z")
	if err != nil || completed {
		t.Fatalf("dirty 未清空不得完成: %v %v", completed, err)
	}
}

// TestOverlayListDirtyLimitClamp 覆盖 limit 上限归一化。
func TestOverlayListDirtyLimitClamp(t *testing.T) {
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	store := newOverlayStore(t, server)
	ctx := context.Background()
	// 空队列 + 超上限 limit → 归一化后仍为空（覆盖 >1000 clamp 分支）。
	entries, err := store.ListDirtyEntries(ctx, 5000)
	if err != nil || len(entries) != 0 {
		t.Fatalf("超上限 limit 归一化: %v %v", entries, err)
	}
	// LoadSnapshots 的 chunk 边界（正好 100 个 id）。
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = "acc-" + itoa(i)
	}
	snapshots, err := store.LoadSnapshots(ctx, ids)
	if err != nil || len(snapshots) != 100 {
		t.Fatalf("100 id 单批读取: %d %v", len(snapshots), err)
	}
}

// TestAdapterNilStore 覆盖 nil 包装。
func TestAdapterNilStore(t *testing.T) {
	if NewOpsJobsStore(nil) != nil {
		t.Fatal("nil store 必须返回 nil 适配器")
	}
}
