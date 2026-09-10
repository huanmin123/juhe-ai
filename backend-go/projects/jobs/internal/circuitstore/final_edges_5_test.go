package circuitstore

// 收尾五：系统性错误出口覆盖——通过删除依赖表构造 SQL 失败，验证各读面
// 在底层损坏时 fail-fast（不静默降级）。每个用例独立 fixture，互不污染。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	_ "modernc.org/sqlite"
)

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
			if _, err := db.Exec(`DROP TABLE ` + tc.drop); err != nil {
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

// TestLoaderErrorSurfacesOnMissingTables 覆盖 loader 各读面在统计/业务表缺失
// 时的 fail-closed 错误出口。
func TestLoaderErrorSurfacesOnMissingTables(t *testing.T) {
	cases := []struct {
		name   string
		drop   string
		action func(loader *ProjectionItemLoader, ctx context.Context) error
	}{
		{
			name: "usage汇总表缺失", drop: "usage_stats_daily",
			action: func(loader *ProjectionItemLoader, ctx context.Context) error {
				timezone, _ := loader.timezone.StatsTimezone(ctx)
				_, _, err := loader.loadUsageSummaries(ctx, []managementRow{newSourcesRow("acc-1")}, loader.now(), timezone)
				return err
			},
		},
		{
			name: "quota成本表缺失", drop: "usage_stats_totals",
			action: func(loader *ProjectionItemLoader, ctx context.Context) error {
				timezone, _ := loader.timezone.StatsTimezone(ctx)
				limits := parseQuotaLimits(`{"total":{"enabled":true,"limit":1}}`)
				_, err := loader.loadQuotaCosts(ctx, []quotaCostCheck{{
					authorizationID: "a", systemAccountID: "sys-1", scopeType: "s", scopeID: "i", limits: limits,
				}}, loader.now(), timezone)
				return err
			},
		},
		{
			name: "团队授权grant表缺失", drop: "resource_authorization_grants",
			action: func(loader *ProjectionItemLoader, ctx context.Context) error {
				team := authorizedSourcesRow("acc-1")
				team.authorizationEffectiveSourceTeamID.Valid = true
				team.authorizationEffectiveSourceTeamID.String = "team-1"
				_, err := loader.loadTeamLimitJSON(ctx, []managementRow{team}, loader.now())
				return err
			},
		},
		{
			name: "余额快照表缺失", drop: "account_usage_snapshots",
			action: func(loader *ProjectionItemLoader, ctx context.Context) error {
				_, err := loader.loadBalanceSnapshotRecords(ctx, []string{"acc-1"})
				return err
			},
		},
		{
			name: "key运行态表缺失", drop: "account_api_key_runtime_states",
			action: func(loader *ProjectionItemLoader, ctx context.Context) error {
				_, err := loader.loadAPIKeyRuntimeStates(ctx, []string{"fp-1"})
				return err
			},
		},
		{
			name: "circuit账本表缺失", drop: "account_circuit_incidents",
			action: func(loader *ProjectionItemLoader, ctx context.Context) error {
				_, err := loader.loadCircuitSummaries(ctx, []string{"acc-1"}, loader.now())
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newLoaderTestDB(t)
			loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
			ctx := context.Background()
			if _, err := db.Exec(`DROP TABLE ` + tc.drop); err != nil {
				t.Fatal(err)
			}
			if err := tc.action(loader, ctx); err == nil {
				t.Fatal("底层表缺失必须报错（fail closed）")
			}
		})
	}
}

// TestHydratePageFailsOnBrokenReads 覆盖水合链剩余读面（usage/quota/balance/
// apiKey/circuit）失败的 fail-closed 行为。
func TestHydratePageFailsOnBrokenReads(t *testing.T) {
	cases := []struct {
		name string
		drop string
	}{
		{name: "usage失败", drop: "usage_stats_daily"},
		{name: "circuit失败", drop: "account_circuit_incidents"},
		{name: "余额快照失败", drop: "account_usage_snapshots"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newLoaderTestDB(t)
			seedOwnerAccount(t, db)
			if _, err := db.Exec(`DROP TABLE ` + tc.drop); err != nil {
				t.Fatal(err)
			}
			loader := newTestLoader(db, stubConcurrency{}, stubRuntime{})
			row := newSourcesRow("acct-1")
			// 余额快照读面仅在 balanceQueryEnabled 行触发；其余读面对所有行生效。
			row.balanceQueryEnabled = 1
			page := &managementPage{rows: []managementRow{row}}
			if _, err := loader.hydratePage(context.Background(), page, time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)); err == nil {
				t.Fatal("读面失败必须 fail closed")
			}
		})
	}
}

// TestControlPlaneErrorSurfacesOnMissingTables 覆盖控制面各方法的错误出口。
func TestControlPlaneErrorSurfacesOnMissingTables(t *testing.T) {
	cases := []struct {
		name   string
		drop   string
		action func(repo *ControlPlaneRepo, ctx context.Context) error
	}{
		{
			name: "outbox表缺失Claim", drop: "account_circuit_outbox",
			action: func(repo *ControlPlaneRepo, ctx context.Context) error {
				_, err := repo.Claim(ctx, "owner", 1000, 30_000, 10)
				return err
			},
		},
		{
			name: "accounts表缺失ListForRebuild", drop: "accounts",
			action: func(repo *ControlPlaneRepo, ctx context.Context) error {
				_, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{Limit: 10, NowMS: 1000})
				return err
			},
		},
		{
			name: "accounts表缺失ListByRuntimeKeys", drop: "accounts",
			action: func(repo *ControlPlaneRepo, ctx context.Context) error {
				_, err := repo.ListByRuntimeKeys(ctx, []string{"acc-1"}, false, 1000)
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, db := openControlPlaneFixture(t)
			ctx := context.Background()
			if _, err := db.Exec(`DROP TABLE ` + tc.drop); err != nil {
				t.Fatal(err)
			}
			if err := tc.action(repo, ctx); err == nil {
				t.Fatal("底层表缺失必须报错")
			}
		})
	}
}

// TestAckAndReleaseErrorSurfaces 覆盖 Ack / ReleaseForReplay 的 SQL 失败出口。
func TestAckAndReleaseErrorSurfaces(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`DROP TABLE account_circuit_outbox`); err != nil {
		t.Fatal(err)
	}
	claim := opsjobs.OutboxEvent{EventID: "e", ClaimToken: "t", ProjectionKey: ProjectionKey}
	if _, err := repo.Ack(ctx, claim, 1000); err == nil {
		t.Fatal("outbox 表缺失 Ack 必须报错")
	}
	if err := repo.ReleaseForReplay(ctx, claim, "reason", 2000, 500); err == nil {
		t.Fatal("outbox 表缺失 Release 必须报错")
	}
}

// TestOverlayExistingAccountsOnMissingAccounts 覆盖对账存在性检查的 SQL 失败。
func TestOverlayExistingAccountsOnMissingAccounts(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	server := newMiniredisForTest(t)
	store := newOverlayStore(t, server)
	reconciler := NewOverlayReconciler(store, repo)
	ctx := context.Background()
	if _, err := db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.ExistingAccountIDs(ctx, []string{"acc-1"}); err == nil {
		t.Fatal("accounts 表缺失必须报错")
	}
}

// TestControlPlaneCursorErrorSurface 覆盖游标 store 的错误出口。
func TestControlPlaneCursorErrorSurface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
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
