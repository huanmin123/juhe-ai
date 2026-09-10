package circuitstore

// 收尾四：适配器错误出口（底层 Lua 调用失败时的透传）与事务失败路径
// （句柄关闭 / 约束破坏时的 fail-fast）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// TestOpsAdapterErrorPassthrough 覆盖 adapter 各方法在底层失败时的错误出口。
func TestOpsAdapterErrorPassthrough(t *testing.T) {
	scope := accountScope("acc-adapter-err")
	stub := newEvalStub(t, nil, errors.New("conn refused"))
	wire := &RedisStore{client: stub, now: fixedNow(), capacity: 4}
	adapter := NewOpsJobsStore(wire)
	ctx := context.Background()
	identity := opsjobs.CircuitTransitionIdentity{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "tr", NowMS: 1000}
	lease := opsjobs.CircuitLeaseSpec{LeaseID: "l", LeaseUntilMS: 9000}
	completion := opsjobs.CircuitCompletion{Outcome: opsjobs.CircuitVerdictFramingComplete}

	if _, err := adapter.Get(ctx, scope, 1000); err == nil {
		t.Fatal("Get 失败必须透传")
	}
	if _, err := adapter.Restore(ctx, opsjobs.CircuitState{Scope: scope, Phase: opsjobs.CircuitPhaseSuspect}, 1000); err == nil {
		t.Fatal("Restore 失败必须透传")
	}
	if _, err := adapter.ListDue(ctx, 1000, 5); err == nil {
		t.Fatal("ListDue 失败必须透传")
	}
	if _, err := adapter.AcquireConfirmationLease(ctx, identity, lease); err == nil {
		t.Fatal("AcquireConfirmationLease 失败必须透传")
	}
	if _, err := adapter.AcquireCanaryLease(ctx, identity, lease); err == nil {
		t.Fatal("AcquireCanaryLease 失败必须透传")
	}
	if _, err := adapter.CompleteConfirmation(ctx, identity, "l", completion); err == nil {
		t.Fatal("CompleteConfirmation 失败必须透传")
	}
	if _, err := adapter.CompleteCanary(ctx, identity, "l", completion); err == nil {
		t.Fatal("CompleteCanary 失败必须透传")
	}
	if _, err := adapter.ReplaceDispatchRevision(ctx, scope, "6", "tr", 1000); err == nil {
		t.Fatal("ReplaceDispatchRevision 失败必须透传")
	}
	if _, err := adapter.ReplaceAccountDispatchRevision(ctx, "acc-adapter-err", "6", "tr", 1000); err == nil {
		t.Fatal("ReplaceAccountDispatchRevision 失败必须透传")
	}
}

// TestNewListAvailabilityRepoNilDB 覆盖构造校验。
func TestNewListAvailabilityRepoNilDB(t *testing.T) {
	if _, err := NewListAvailabilityRepo(ListAvailabilityConfig{}); err == nil {
		t.Fatal("缺 DB 必须报错")
	}
}

// TestRepoTxFailurePaths 覆盖事务开启失败（句柄已关闭）时的错误出口。
func TestRepoTxFailurePaths(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnqueueMissing(ctx, 10, 1000); err == nil {
		t.Fatal("句柄关闭后 EnqueueMissing 必须报错")
	}
	if _, err := repo.EnqueueDue(ctx, 10, 1000); err == nil {
		t.Fatal("句柄关闭后 EnqueueDue 必须报错")
	}
	if _, err := repo.EnqueueAllForRuntimeRecovery(ctx, 1000); err == nil {
		t.Fatal("句柄关闭后 EnqueueAllForRuntimeRecovery 必须报错")
	}
	if err := repo.RefreshViewerHealth(ctx, "viewer-1", "2026-09-04T10:00:00.000Z"); err == nil {
		t.Fatal("句柄关闭后 RefreshViewerHealth 必须报错")
	}
	if _, err := repo.ClaimDirty(ctx, "owner", 10, 1000, 1000); err == nil {
		t.Fatal("句柄关闭后 ClaimDirty 必须报错")
	}
	if _, err := repo.ApplyDeletionClaim(ctx, opsjobs.DirtyClaim{AccountID: "a", Generation: 1, ClaimToken: "t"}); err == nil {
		t.Fatal("句柄关闭后 ApplyDeletionClaim 必须报错")
	}
}

// TestApplyClaimsUpsertFailurePaths 覆盖投影 upsert 子语句失败时的整体回滚。
func TestApplyClaimsUpsertFailurePaths(t *testing.T) {
	for _, dropTable := range []string{
		"account_list_availability_projection_index",
		"account_list_availability_runtime_overlays",
		"account_list_availability_projection_tags",
		"account_list_availability_projection_search_terms",
	} {
		t.Run(dropTable, func(t *testing.T) {
			repo, db := openListAvailabilityFixture(t)
			ctx := context.Background()
			now := timeDateHelper()
			nowMS := now.UnixMilli()
			if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'viewer-1')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				INSERT INTO account_list_availability_dirty (
					account_id, viewer_system_account_id, generation, applied_generation, reason,
					available_at_ms, attempt_count, created_at_ms, updated_at_ms
				) VALUES ('acc-1', 'viewer-1', 1, 0, 'projection_missing', 0, 0, ?, ?)`, nowMS, nowMS); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TABLE ` + dropTable); err != nil {
				t.Fatal(err)
			}
			claims, err := repo.ClaimDirty(ctx, "owner", 10, 30_000, nowMS)
			if err != nil || len(claims) != 1 {
				t.Fatalf("认领失败: %v %v", claims, err)
			}
			if _, err := repo.ApplyClaims(ctx, []opsjobs.ProjectionWrite{{
				Claim: claims[0],
				Item:  validProjectionItem("acc-1"),
				Scope: opsjobs.ProjectionScope{ViewerSystemAccountID: "viewer-1", AccountID: "acc-1", CreatedAt: &[]string{"2026-01-01T00:00:00.000Z"}[0]},
				Now:   now,
			}}); err == nil {
				t.Fatalf("子语句失败必须整体报错: %s", dropTable)
			}
		})
	}
}

func timeDateHelper() time.Time {
	return time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
}

// 防止 redis 导入在后续裁剪中悬空（evalStub 与本文件同包复用）。
