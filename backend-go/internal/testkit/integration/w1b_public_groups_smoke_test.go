//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/publicgroups"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicGroupsPostgresSmoke(t *testing.T) {
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

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	ids := map[string]int{}
	service := publicgroups.NewService(publicgroups.Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID: func(prefix string) string {
			ids[prefix]++
			return prefix + "_w1b_smoke_" + strconv.Itoa(ids[prefix])
		},
	})

	added, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "admin",
		TargetDisplayName: "管理员",
		Name:              "福利",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("add public group: %v", err)
	}
	if added.Action != "created" || !added.Target.Created || added.Group == nil || added.Group.Name != "福利" {
		t.Fatalf("added group = %+v", added)
	}

	existing, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "福利",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("add existing public group: %v", err)
	}
	if existing.Action != "existing" || existing.Group == nil || existing.Group.ID != added.Group.ID {
		t.Fatalf("existing group = %+v, first = %+v", existing, added)
	}

	benefits, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "Benefits",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("add ascii public group: %v", err)
	}
	if _, err := store.CreatePublicGroup(ctx, port.PublicGroupCreateInput{
		ID:              "grp_w1b_smoke_duplicate",
		SystemAccountID: benefits.Target.SystemAccountID,
		Name:            "benefits",
		ProviderCode:    "gpt",
		Enabled:         true,
		GroupType:       publicgroups.DefaultGroupType,
		Now:             now,
	}); !errors.Is(err, port.ErrPublicGroupDuplicateName) {
		t.Fatalf("create duplicate lowercase group error = %v, want ErrPublicGroupDuplicateName", err)
	}

	listed, err := service.List(ctx, publicgroups.ListInput{
		TargetUsername: "admin",
		ProviderCode:   "gpt",
		Keyword:        "福",
		Page:           1,
		PageSize:       10,
	})
	if err != nil {
		t.Fatalf("list public groups: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != added.Group.ID || listed.Target.SystemAccountID == "" {
		t.Fatalf("listed groups = %+v", listed)
	}

	accountBound, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "账号绑定分组",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("add account-bound public group: %v", err)
	}
	insertW1bGroupAccount(t, ctx, db, listed.Target.SystemAccountID, accountBound.Group.ID, "acct_w1b_smoke", now)
	nextProvider := "openai"
	if _, err := service.Update(ctx, publicgroups.UpdateInput{
		GroupID:      accountBound.Group.ID,
		ProviderCode: &nextProvider,
	}); !errors.Is(err, publicgroups.ErrGroupProviderHasAccount) {
		t.Fatalf("update provider for account-bound group error = %v, want ErrGroupProviderHasAccount", err)
	}

	otherTarget, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "other",
		TargetDisplayName: "其他用户",
		Name:              "其他用户分组",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("add other target group: %v", err)
	}
	newName := "越权修改"
	if _, err := service.Update(ctx, publicgroups.UpdateInput{
		TargetUsername: &otherTarget.Target.Username,
		GroupID:        added.Group.ID,
		Name:           &newName,
	}); !errors.Is(err, publicgroups.ErrGroupNotFound) {
		t.Fatalf("update with wrong target error = %v, want ErrGroupNotFound", err)
	}

	insertW1bRouteStrategyGroup(t, ctx, db, listed.Target.SystemAccountID, "route_w1b_smoke", "rsg_w1b_smoke_1", added.Group.ID, now)
	if _, err := service.Delete(ctx, publicgroups.DeleteInput{GroupID: added.Group.ID}); !errors.Is(err, publicgroups.ErrRouteStrategyWouldLose) {
		t.Fatalf("delete protected group error = %v, want ErrRouteStrategyWouldLose", err)
	}

	backup, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "备用",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("add backup public group: %v", err)
	}
	insertW1bRouteStrategyBinding(t, ctx, db, listed.Target.SystemAccountID, "route_w1b_smoke", "rsg_w1b_smoke_2", backup.Group.ID, now)

	deleted, err := service.Delete(ctx, publicgroups.DeleteInput{GroupID: added.Group.ID})
	if err != nil {
		t.Fatalf("delete public group after backup binding: %v", err)
	}
	if deleted.Action != "deleted" || deleted.Group == nil || deleted.Group.ID != added.Group.ID {
		t.Fatalf("deleted group = %+v", deleted)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM juhe_business.groups WHERE id = $1", added.Group.ID).Scan(&count); err != nil {
		t.Fatalf("count deleted group: %v", err)
	}
	if count != 0 {
		t.Fatalf("deleted group count = %d, want 0", count)
	}

	assertW1bRouteStrategyOwnerMismatchRejected(t, ctx, db, listed.Target.SystemAccountID, otherTarget.Target.SystemAccountID, backup.Group.ID, now)
	assertW1bConcurrentRouteStrategyDeleteProtection(t, ctx, db, service, listed.Target.SystemAccountID, now)
}

func insertW1bGroupAccount(t *testing.T, ctx context.Context, db dbExecutor, ownerID string, groupID string, accountID string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, credential_mask, concurrency_limit, priority,
			client_compatibility, schedulable, created_at, updated_at
		) VALUES (
			$1, $2, 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1',
			$3, 'api_key', 'active', 'v1:test:test:test', 'sk***test', 20, 0,
			'openai_standard', true, $4, $5
		)
		ON CONFLICT (id) DO NOTHING
	`, accountID, ownerID, accountID, now, now)
	if err != nil {
		t.Fatalf("insert account for group binding: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_accounts (
			system_account_id, group_id, account_id, enabled, created_at, updated_at
		) VALUES ($1, $2, $3, true, $4, $5)
	`, ownerID, groupID, accountID, now, now)
	if err != nil {
		t.Fatalf("insert group account: %v", err)
	}
}

func insertW1bRouteStrategyGroup(t *testing.T, ctx context.Context, db dbExecutor, ownerID string, routeID string, bindingID string, groupID string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategies (
			id, system_account_id, name, mode, status, is_default, created_at, updated_at
		) VALUES ($1, $2, $3, 'normal', 'active', false, $4, $5)
	`, routeID, ownerID, routeID, now, now)
	if err != nil {
		t.Fatalf("insert route strategy: %v", err)
	}
	insertW1bRouteStrategyBinding(t, ctx, db, ownerID, routeID, bindingID, groupID, now)
}

func insertW1bRouteStrategyBinding(t *testing.T, ctx context.Context, db dbExecutor, ownerID string, routeID string, bindingID string, groupID string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 1, 1, 'active', $5, $6)
	`, bindingID, routeID, ownerID, groupID, now, now)
	if err != nil {
		t.Fatalf("insert route strategy group: %v", err)
	}
}

func assertW1bRouteStrategyOwnerMismatchRejected(t *testing.T, ctx context.Context, db *sql.DB, ownerID string, otherOwnerID string, groupID string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategy_groups (
			id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 1, 1, 'active', $5, $6)
	`, "rsg_w1b_owner_mismatch", "route_w1b_smoke", otherOwnerID, groupID, now, now)
	if err == nil {
		t.Fatalf("insert owner-mismatched route strategy group succeeded; want FK failure for owner %s group owner %s", otherOwnerID, ownerID)
	}
}

func assertW1bConcurrentRouteStrategyDeleteProtection(t *testing.T, ctx context.Context, db *sql.DB, service *publicgroups.Service, ownerID string, now time.Time) {
	t.Helper()

	first, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "并发保护一",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("add concurrent first group: %v", err)
	}
	second, err := service.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "并发保护二",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("add concurrent second group: %v", err)
	}

	insertW1bRouteStrategyGroup(t, ctx, db, ownerID, "route_w1b_concurrent", "rsg_w1b_concurrent_1", first.Group.ID, now)
	insertW1bRouteStrategyBinding(t, ctx, db, ownerID, "route_w1b_concurrent", "rsg_w1b_concurrent_2", second.Group.ID, now)

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, groupID := range []string{first.Group.ID, second.Group.ID} {
		wg.Add(1)
		go func(groupID string) {
			defer wg.Done()
			_, err := service.Delete(ctx, publicgroups.DeleteInput{GroupID: groupID})
			errs <- err
		}(groupID)
	}
	wg.Wait()
	close(errs)

	successes := 0
	protected := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, publicgroups.ErrRouteStrategyWouldLose):
			protected++
		default:
			t.Fatalf("concurrent delete error = %v", err)
		}
	}
	if successes != 1 || protected != 1 {
		t.Fatalf("concurrent delete results = success %d protected %d, want 1/1", successes, protected)
	}

	var enabledGroups int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM juhe_business.route_strategy_groups AS bindings
		JOIN juhe_business.groups AS groups
		  ON groups.id = bindings.group_id
		  AND groups.system_account_id = bindings.system_account_id
		WHERE bindings.route_strategy_id = 'route_w1b_concurrent'
		  AND bindings.status = 'active'
		  AND groups.enabled = true
	`).Scan(&enabledGroups); err != nil {
		t.Fatalf("count concurrent route enabled groups: %v", err)
	}
	if enabledGroups != 1 {
		t.Fatalf("concurrent route enabled groups = %d, want 1", enabledGroups)
	}
}

type dbExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
