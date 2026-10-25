package circuitstore

// 收尾五：系统性错误出口覆盖——通过删除依赖表构造 SQL 失败，验证各读面
// 在底层损坏时 fail-fast（不静默降级）。每个用例独立 fixture，互不污染。

import (
	"context"
	"database/sql"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// kitDropBusinessTable 生成带 juhe_business schema 限定的 DROP 语句（repo 族
// PG fixture 中业务表位于 juhe_business schema，测试体 DROP 与生产 schema 限
// 定对齐；loader 族组B保留 SQLite，不走本 helper）。
func kitDropBusinessTable(table string) string {
	return `DROP TABLE juhe_business.` + table
}

// TestRepoErrorSurfacesOnMissingTables 通过删表构造 SQL 失败，验证投影 repo
// 各方法的错误出口。
func TestRepoErrorSurfacesOnMissingTables(t *testing.T) {
	cases := []struct {
		name   string
		drop   string
		action func(repo *ListAvailabilityRepo, ctx context.Context) error
	}{
		{
			name: "依赖健康表缺失Ensure", drop: "account_list_availability_projection_dependency_health",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				return repo.EnsureRuntimeDependency(ctx, "2026-09-04T10:00:00.000Z")
			},
		},
		{
			name: "依赖健康表缺失Touch", drop: "account_list_availability_projection_dependency_health",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				return repo.TouchRuntimeDependency(ctx, "2026-09-04T10:00:00.000Z")
			},
		},
		{
			name: "依赖健康表缺失Mark", drop: "account_list_availability_projection_dependency_health",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				return repo.MarkRuntimeDependencyUnavailable(ctx, "reason", "2026-09-04T10:00:00.000Z")
			},
		},
		{
			name: "依赖健康表缺失Begin", drop: "account_list_availability_projection_dependency_health",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.BeginRuntimeDependencyRecovery(ctx, "2026-09-04T10:00:00.000Z")
				return err
			},
		},
		{
			name: "依赖健康表缺失Complete", drop: "account_list_availability_projection_dependency_health",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.CompleteRuntimeDependencyRecovery(ctx, "2026-09-04T10:00:00.000Z")
				return err
			},
		},
		{
			name: "viewer健康表缺失Ensure", drop: "account_list_availability_projection_viewer_health",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.EnsureViewerHealth(ctx, 10, "2026-09-04T10:00:00.000Z")
				return err
			},
		},
		{
			name: "viewer健康表缺失Candidates", drop: "account_list_availability_projection_viewer_health",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.ListViewerHealthRefreshCandidates(ctx, 10)
				return err
			},
		},
		{
			name: "投影表缺失RefreshViewerHealth", drop: "account_list_availability_projections",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				return repo.RefreshViewerHealth(ctx, "viewer-1", "2026-09-04T10:00:00.000Z")
			},
		},
		{
			name: "账户表缺失ListScopes", drop: "accounts",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.ListScopes(ctx, []string{"acc-1"})
				return err
			},
		},
		{
			name: "搜索文档表缺失LoadSearchTerms", drop: "account_name_search_documents",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.LoadSearchTerms(ctx, []string{"acc-1"})
				return err
			},
		},
		{
			name: "dirty表缺失ClaimDirty", drop: "account_list_availability_dirty",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.ClaimDirty(ctx, "owner", 10, 1000, 1000)
				return err
			},
		},
		{
			name: "dirty表缺失ReleaseForReplay", drop: "account_list_availability_dirty",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.ReleaseForReplay(ctx, replayInputHelper("a", 1, "t"))
				return err
			},
		},
		{
			name: "dirty表缺失EnqueueMissing", drop: "account_list_availability_dirty",
			action: func(repo *ListAvailabilityRepo, ctx context.Context) error {
				_, err := repo.EnqueueMissing(ctx, 10, 1000)
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, db := openListAvailabilityFixture(t)
			ctx := context.Background()
			if _, err := db.Exec(kitDropBusinessTable(tc.drop)); err != nil {
				t.Fatal(err)
			}
			if err := tc.action(repo, ctx); err == nil {
				t.Fatal("底层表缺失必须报错（fail-fast）")
			}
		})
	}
}

func replayInputHelper(accountID string, generation int64, token string) opsjobs.ListAvailabilityReplayInput {
	return opsjobs.ListAvailabilityReplayInput{AccountID: accountID, Generation: generation, ClaimToken: token, Reason: "r", NowMS: 1000}
}

func TestOverlayExistingAccountsOnMissingAccounts(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	server := newMiniredisForTest(t)
	store := newOverlayStore(t, server)
	reconciler := NewOverlayReconciler(store, repo)
	ctx := context.Background()
	if _, err := db.Exec(`DROP TABLE juhe_business.accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.ExistingAccountIDs(ctx, []string{"acc-1"}); err == nil {
		t.Fatal("accounts 表缺失必须报错")
	}
}

// TestControlPlaneCursorErrorSurface 覆盖游标 store 的错误出口。
func TestControlPlaneCursorErrorSurface(t *testing.T) {
	db := openKitPostgresDB(t, func(ctx context.Context, raw *sql.DB) error { return nil })
	repo, err := NewControlPlaneRepo(ControlPlaneConfig{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	// 关闭句柄后建表必须报错。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureCursorSchema(context.Background()); err == nil {
		t.Fatal("句柄关闭后建表必须报错")
	}
	s, err := NewReconcileCursorStore(ControlPlaneConfig{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(context.Background(), opsjobs.IncidentCursor{}); err == nil {
		t.Fatal("句柄关闭后 Save 必须报错")
	}
}

func newMiniredisForTest(t *testing.T) *miniredisServer {
	t.Helper()
	return miniredisRunForTest(t)
}

func miniredisRunForTest(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	return miniredis.RunT(t)
}

type miniredisServer = miniredis.Miniredis
