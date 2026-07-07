//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/managementoperationlogs"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementOperationLogsPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	insertW2OperationLogViewerAccountFixture(t, ctx, db, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	insertW2OperationLogFixture(t, ctx, store, now)

	service := managementoperationlogs.NewService(store)
	adminList, err := service.List(ctx, managementoperationlogs.ListInput{PageSize: 10})
	if err != nil {
		t.Fatalf("admin operation log list: %v", err)
	}
	if len(adminList.Items) != 2 || adminList.Total != 2 {
		t.Fatalf("admin list = %+v", adminList)
	}
	if adminList.Items[0].ID != "oplog_w2_all_users" || adminList.Items[1].ID != "oplog_w2_targeted" {
		t.Fatalf("admin list order = %+v", adminList.Items)
	}

	searchList, err := service.List(ctx, managementoperationlogs.ListInput{SummaryKeyword: "keywordneedle", PageSize: 10})
	if err != nil {
		t.Fatalf("admin operation log search: %v", err)
	}
	if len(searchList.Items) != 1 || searchList.Items[0].ID != "oplog_w2_targeted" {
		t.Fatalf("search list = %+v", searchList)
	}

	viewerList, err := service.List(ctx, managementoperationlogs.ListInput{
		ViewerSystemAccountID: "sys_w2_operation_viewer",
		PageSize:              10,
	})
	if err != nil {
		t.Fatalf("viewer operation log list: %v", err)
	}
	if len(viewerList.Items) != 2 {
		t.Fatalf("viewer list = %+v", viewerList)
	}
	for _, item := range viewerList.Items {
		if item.ClientIP != "" {
			t.Fatalf("viewer list leaked client ip: %+v", item)
		}
		if item.ID == "oplog_w2_targeted" && (item.Method != "" || item.StatusCode != nil || item.DetailLevel != "summary") {
			t.Fatalf("viewer summary item not sanitized: %+v", item)
		}
	}

	adminDetail, found, err := service.Detail(ctx, managementoperationlogs.DetailInput{ID: "oplog_w2_targeted"})
	if err != nil || !found {
		t.Fatalf("admin detail found=%v err=%v", found, err)
	}
	if len(adminDetail.Changes) != 1 || len(adminDetail.Targets) == 0 || len(adminDetail.Viewers) == 0 || adminDetail.ClientIP == "" || adminDetail.UserAgent == "" {
		t.Fatalf("admin detail = %+v", adminDetail)
	}

	viewerDetail, found, err := service.Detail(ctx, managementoperationlogs.DetailInput{
		ID:                    "oplog_w2_targeted",
		ViewerSystemAccountID: "sys_w2_operation_viewer",
	})
	if err != nil || !found {
		t.Fatalf("viewer detail found=%v err=%v", found, err)
	}
	if viewerDetail.ClientIP != "" || viewerDetail.UserAgent != "" || viewerDetail.Method != "" || len(viewerDetail.Changes) != 0 || len(viewerDetail.Targets) != 0 || len(viewerDetail.Viewers) != 0 {
		t.Fatalf("viewer detail not sanitized: %+v", viewerDetail)
	}
}

func insertW2OperationLogViewerAccountFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w2_operation_viewer', 'w2-operation-viewer', 'W2 Operation Viewer', NULL, 'user', 'active', 'hash',
			false, false, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 operation log viewer account: %v", err)
	}
}

func insertW2OperationLogFixture(t *testing.T, ctx context.Context, store *postgresstore.Store, now time.Time) {
	t.Helper()
	status := 200
	if err := store.InsertOperationLog(ctx, port.OperationLogInput{
		ID:                            "oplog_w2_targeted",
		TraceID:                       "req_w2_operation_targeted",
		ActorSystemAccountID:          "sys_w2_proxy_options",
		ActorUsername:                 "w2-proxy-options",
		ActorDisplayName:              "W2 Proxy Options",
		ActorRole:                     "admin",
		OperationScopeSystemAccountID: "sys_w2_operation_viewer",
		Mode:                          "admin",
		Module:                        "accounts",
		Action:                        "update_tags",
		OperationKey:                  "accounts.update_tags",
		ResourceType:                  "account",
		ResourceID:                    "acct_w2_operation",
		ResourceName:                  "W2 Operation Account",
		Summary:                       "更新账户标签 keywordneedle",
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       []port.OperationLogChange{{Field: "tags", Label: "标签", Before: []string{"旧"}, After: []string{"新"}}},
		Metadata:                      map[string]any{"source": "integration"},
		Method:                        "PATCH",
		Path:                          "/__aisys__/api/accounts/acct_w2_operation/tags",
		StatusCode:                    &status,
		ClientIP:                      "127.0.0.1",
		UserAgent:                     "integration",
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  "sys_w2_operation_viewer",
			VisibilityReason: "resource_owner",
			DetailLevel:      "summary",
		}},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert targeted operation log: %v", err)
	}

	if err := store.InsertOperationLog(ctx, port.OperationLogInput{
		ID:                   "oplog_w2_all_users",
		TraceID:              "req_w2_operation_all",
		ActorSystemAccountID: "sys_w2_proxy_options",
		ActorUsername:        "w2-proxy-options",
		ActorDisplayName:     "W2 Proxy Options",
		ActorRole:            "admin",
		Mode:                 "admin",
		Module:               "system",
		Action:               "notice",
		OperationKey:         "system.notice",
		ResourceType:         "system",
		Summary:              "全局操作日志公告",
		DetailLevel:          "full",
		VisibilityScope:      "all_users",
		Metadata:             map[string]any{"source": "integration"},
		Method:               "POST",
		Path:                 "/__aisys__/api/notices",
		StatusCode:           &status,
		ClientIP:             "127.0.0.1",
		UserAgent:            "integration",
		CreatedAt:            now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("insert all-users operation log: %v", err)
	}
}
