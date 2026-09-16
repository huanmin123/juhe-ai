package circuitstore

// w12f_closed_arms_test.go 批量覆盖控制面与投影 repo 在句柄关闭后的
// fail-closed 第一错误臂（每个公开方法一次原始错误传播）。
//
// w12f 收尾时未覆盖清单（距 95% 尚差约 100 条，未登记为不可达，后续 wave
// 可继续）：controlplane.go Claim/Ack/ReleaseForReplay 的 outbox 租约与
// 重试退避分支、listavailability.go ClaimDirty/ApplyClaims/
// upsertProjectionTx 的围栏与幂等分支、store.go JSON 状态机的损坏载荷
// 分支——均需构造多行精确状态的事务内 fixture。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

func TestW12fControlPlaneClosedArms(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{NowMS: 1000, Limit: 1}); err == nil {
		t.Fatal("ListForRebuild 必须报错")
	}
	if _, err := repo.ListByRuntimeKeys(ctx, []string{"w12f-key"}, false, 1000); err == nil {
		t.Fatal("ListByRuntimeKeys 必须报错")
	}
	if _, err := repo.GetByScopeKey(ctx, "w12f-scope"); err == nil {
		t.Fatal("GetByScopeKey 必须报错")
	}
	if _, err := repo.Claim(ctx, "w12f-owner", 1000, 30_000, 10); err == nil {
		t.Fatal("Claim 必须报错")
	}
	if _, err := repo.Ack(ctx, opsjobs.OutboxEvent{}, 1000); err == nil {
		t.Fatal("Ack 必须报错")
	}
	if err := repo.ReleaseForReplay(ctx, opsjobs.OutboxEvent{}, "w12f-class", 1000, 1000); err == nil {
		t.Fatal("ReleaseForReplay 必须报错")
	}
}

func TestW12fListAvailabilityClosedArms(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{"EnsureRuntimeDependency", func() error { return repo.EnsureRuntimeDependency(ctx, isoMillisW12f(time.Now()) ) }},
		{"TouchRuntimeDependency", func() error { return repo.TouchRuntimeDependency(ctx, isoMillisW12f(time.Now())) }},
		{"MarkRuntimeDependencyUnavailable", func() error { return repo.MarkRuntimeDependencyUnavailable(ctx, "w12f-reason", isoMillisW12f(time.Now())) }},
		{"BeginRuntimeDependencyRecovery", func() error { _, err := repo.BeginRuntimeDependencyRecovery(ctx, isoMillisW12f(time.Now())); return err }},
		{"CompleteRuntimeDependencyRecovery", func() error { _, err := repo.CompleteRuntimeDependencyRecovery(ctx, isoMillisW12f(time.Now())); return err }},
		{"EnqueueMissing", func() error { _, err := repo.EnqueueMissing(ctx, 10, 1000); return err }},
		{"EnqueueDue", func() error { _, err := repo.EnqueueDue(ctx, 10, 1000); return err }},
		{"EnqueueAllForRuntimeRecovery", func() error { _, err := repo.EnqueueAllForRuntimeRecovery(ctx, 1000); return err }},
		{"EnsureViewerHealth", func() error { _, err := repo.EnsureViewerHealth(ctx, 10, isoMillisW12f(time.Now())); return err }},
		{"ListViewerHealthRefreshCandidates", func() error { _, err := repo.ListViewerHealthRefreshCandidates(ctx, 10); return err }},
		{"RefreshViewerHealth", func() error { return repo.RefreshViewerHealth(ctx, "w12f-viewer", isoMillisW12f(time.Now())) }},
		{"ClaimDirty", func() error { _, err := repo.ClaimDirty(ctx, "w12f-owner", 10, 30_000, 1000); return err }},
		{"ListScopes", func() error { _, err := repo.ListScopes(ctx, []string{"w12f-acc"}); return err }},
		{"LoadSearchTerms", func() error { _, err := repo.LoadSearchTerms(ctx, []string{"w12f-acc"}); return err }},
		{"ApplyClaims", func() error { _, err := repo.ApplyClaims(ctx, []opsjobs.ProjectionWrite{{}}); return err }},
		{"ApplyDeletionClaim", func() error { _, err := repo.ApplyDeletionClaim(ctx, opsjobs.DirtyClaim{}); return err }},
		{"ReleaseForReplay", func() error { _, err := repo.ReleaseForReplay(ctx, opsjobs.ListAvailabilityReplayInput{}); return err }},
	}
	for _, tc := range cases {
		if err := tc.run(); err == nil {
			t.Fatalf("%s 在句柄关闭后必须报错", tc.name)
		}
	}
}

func isoMillisW12f(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}
