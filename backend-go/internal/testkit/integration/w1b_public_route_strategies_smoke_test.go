//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/publicgroups"
	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicRouteStrategiesPostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 7, 11, 30, 0, 0, time.UTC)
	var groupSeq int
	groupService := publicgroups.NewService(publicgroups.Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID: func(prefix string) string {
			groupSeq++
			return prefix + "_w1b_route_smoke_" + strconv.Itoa(groupSeq)
		},
	})
	var routeSeq int
	routeService := publicroutestrategies.NewService(publicroutestrategies.Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID: func(prefix string) string {
			routeSeq++
			return prefix + "_w1b_route_smoke_" + strconv.Itoa(routeSeq)
		},
	})

	_, err = routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "missing",
		Name:           "缺失用户策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: "grp_missing"}},
	})
	if !errors.Is(err, publicroutestrategies.ErrTargetNotFound) {
		t.Fatalf("add missing target error = %v, want ErrTargetNotFound", err)
	}

	primary, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "admin",
		TargetDisplayName: "管理员",
		Name:              "主分组",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("add primary group: %v", err)
	}
	backup, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "备用分组",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("add backup group: %v", err)
	}
	disabledFlag := false
	disabledGroup, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername: "admin",
		Name:           "停用分组",
		ProviderCode:   "gpt",
		Enabled:        &disabledFlag,
	})
	if err != nil {
		t.Fatalf("add disabled group: %v", err)
	}
	other, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "other",
		TargetDisplayName: "其他用户",
		Name:              "其他分组",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("add other group: %v", err)
	}

	created, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "公开策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: primary.Group.ID}},
	})
	if err != nil {
		t.Fatalf("add route strategy: %v", err)
	}
	if created.Action != "created" || created.RouteStrategy == nil || created.RouteStrategy.Mode != publicroutestrategies.ModeNormal {
		t.Fatalf("created route strategy = %+v", created)
	}
	routeID := created.RouteStrategy.ID

	if _, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "公开策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: primary.Group.ID}},
	}); !errors.Is(err, publicroutestrategies.ErrDuplicateRouteStrategyName) {
		t.Fatalf("add duplicate route error = %v, want ErrDuplicateRouteStrategyName", err)
	}

	if _, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "停用绑定策略",
		Mode:           publicroutestrategies.ModeWeighted,
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: disabledGroup.Group.ID, Status: publicroutestrategies.StatusActive}},
	}); !errors.Is(err, publicroutestrategies.ErrInvalidBinding) {
		t.Fatalf("add disabled active binding error = %v, want ErrInvalidBinding", err)
	}

	if _, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "跨用户绑定策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: other.Group.ID}},
	}); !errors.Is(err, publicroutestrategies.ErrGroupBoundary) {
		t.Fatalf("add cross-owner binding error = %v, want ErrGroupBoundary", err)
	}

	insertW1bGroupAuthorization(t, ctx, db, "rauth_w1b_route_smoke", other.Group.ID, other.Target.SystemAccountID, created.Target.SystemAccountID, true, now)
	authorizedCreated, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "授权跨用户绑定策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: other.Group.ID}},
	})
	if err != nil {
		t.Fatalf("add authorized cross-owner route strategy: %v", err)
	}
	if authorizedCreated.RouteStrategy == nil || len(authorizedCreated.RouteStrategy.GroupBindings) != 1 {
		t.Fatalf("authorized created route strategy = %+v", authorizedCreated.RouteStrategy)
	}
	authorizedBinding := authorizedCreated.RouteStrategy.GroupBindings[0]
	if authorizedBinding.GroupID != other.Group.ID || authorizedBinding.GroupName != "其他分组" || !authorizedBinding.GroupEnabled {
		t.Fatalf("authorized created binding = %+v", authorizedBinding)
	}

	mode := publicroutestrategies.ModeFailover
	updated, err := routeService.Update(ctx, publicroutestrategies.UpdateInput{
		RouteStrategyID: routeID,
		Mode:            &mode,
		GroupBindings: publicroutestrategies.NewOptionalGroupBindings([]publicroutestrategies.GroupBindingInput{
			{GroupID: backup.Group.ID, Priority: 2, Weight: 50},
			{GroupID: primary.Group.ID, Priority: 1, Weight: 100},
		}, true),
	})
	if err != nil {
		t.Fatalf("update route strategy failover: %v", err)
	}
	if updated.RouteStrategy == nil || updated.RouteStrategy.Mode != publicroutestrategies.ModeFailover || len(updated.RouteStrategy.GroupBindings) != 2 {
		t.Fatalf("updated route strategy = %+v", updated.RouteStrategy)
	}
	if updated.RouteStrategy.GroupBindings[0].GroupID != primary.Group.ID || updated.RouteStrategy.GroupBindings[1].GroupID != backup.Group.ID {
		t.Fatalf("binding order = %+v", updated.RouteStrategy.GroupBindings)
	}

	updatedAuthorized, err := routeService.Update(ctx, publicroutestrategies.UpdateInput{
		RouteStrategyID: routeID,
		Mode:            stringPtr(publicroutestrategies.ModeNormal),
		GroupBindings: publicroutestrategies.NewOptionalGroupBindings([]publicroutestrategies.GroupBindingInput{{
			GroupID: other.Group.ID,
		}}, true),
	})
	if err != nil {
		t.Fatalf("update route strategy to authorized cross-owner group: %v", err)
	}
	if updatedAuthorized.RouteStrategy == nil || len(updatedAuthorized.RouteStrategy.GroupBindings) != 1 || !updatedAuthorized.RouteStrategy.GroupBindings[0].GroupEnabled {
		t.Fatalf("updated authorized route strategy = %+v", updatedAuthorized.RouteStrategy)
	}

	setW1bGroupAuthorizationEnabled(t, ctx, db, "rauth_w1b_route_smoke", false, now.Add(time.Minute))
	listedDisabledAuthorization, err := routeService.List(ctx, publicroutestrategies.ListInput{
		TargetUsername: "admin",
		Keyword:        "公开",
		Page:           1,
		PageSize:       10,
	})
	if err != nil {
		t.Fatalf("list route strategy with disabled authorization: %v", err)
	}
	if len(listedDisabledAuthorization.Items) != 1 || len(listedDisabledAuthorization.Items[0].GroupBindings) != 1 || listedDisabledAuthorization.Items[0].GroupBindings[0].GroupEnabled {
		t.Fatalf("disabled authorization binding summary = %+v", listedDisabledAuthorization.Items)
	}
	if _, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "停用授权绑定策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: other.Group.ID}},
	}); !errors.Is(err, publicroutestrategies.ErrInvalidBinding) {
		t.Fatalf("add disabled authorization binding error = %v, want ErrInvalidBinding", err)
	}

	listed, err := routeService.List(ctx, publicroutestrategies.ListInput{
		TargetUsername: "admin",
		Keyword:        "公开",
		Mode:           "all",
		Status:         publicroutestrategies.StatusActive,
		Page:           1,
		PageSize:       10,
	})
	if err != nil {
		t.Fatalf("list route strategies: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != routeID || listed.Items[0].APIKeyCount != 0 {
		t.Fatalf("listed route strategies = %+v", listed)
	}

	insertW1bRouteStrategySmokeAPIKey(t, ctx, db, updated.Target.SystemAccountID, routeID, now)
	if _, err := routeService.Delete(ctx, publicroutestrategies.DeleteInput{RouteStrategyID: routeID}); !errors.Is(err, publicroutestrategies.ErrRouteStrategyAPIKeysInUse) {
		t.Fatalf("delete route strategy with api key error = %v, want ErrRouteStrategyAPIKeysInUse", err)
	}
	deleteW1bRouteStrategySmokeAPIKey(t, ctx, db, "key_w1b_route_smoke")

	insertW1bDefaultRouteStrategy(t, ctx, db, updated.Target.SystemAccountID, "rts_w1b_route_default", "rsg_w1b_route_default", primary.Group.ID, now)
	if _, err := routeService.Delete(ctx, publicroutestrategies.DeleteInput{RouteStrategyID: "rts_w1b_route_default"}); !errors.Is(err, publicroutestrategies.ErrDefaultRouteStrategyDelete) {
		t.Fatalf("delete default route strategy error = %v, want ErrDefaultRouteStrategyDelete", err)
	}

	deleted, err := routeService.Delete(ctx, publicroutestrategies.DeleteInput{RouteStrategyID: routeID})
	if err != nil {
		t.Fatalf("delete route strategy: %v", err)
	}
	if deleted.Action != "deleted" || deleted.RouteStrategy == nil || deleted.RouteStrategy.ID != routeID {
		t.Fatalf("deleted route strategy = %+v", deleted)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*)::int FROM juhe_business.route_strategies WHERE id = $1", routeID).Scan(&count); err != nil {
		t.Fatalf("count deleted route strategy: %v", err)
	}
	if count != 0 {
		t.Fatalf("deleted route strategy count = %d, want 0", count)
	}
}

func insertW1bRouteStrategySmokeAPIKey(t *testing.T, ctx context.Context, db *sql.DB, ownerID string, routeID string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.api_keys (
			id, system_account_id, name, route_strategy_id, status, is_default, key_hash, key_prefix, key_suffix, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'active', false, $5, $6, $7, $8, $9)
	`, "key_w1b_route_smoke", ownerID, "Route Smoke Key", routeID, "hash_w1b_route_smoke", "jua", "smoke", now, now)
	if err != nil {
		t.Fatalf("insert route smoke api key: %v", err)
	}
}

func deleteW1bRouteStrategySmokeAPIKey(t *testing.T, ctx context.Context, db *sql.DB, id string) {
	t.Helper()

	if _, err := db.ExecContext(ctx, "DELETE FROM juhe_business.api_keys WHERE id = $1", id); err != nil {
		t.Fatalf("delete route smoke api key: %v", err)
	}
}

func insertW1bGroupAuthorization(t *testing.T, ctx context.Context, db *sql.DB, authorizationID string, groupID string, ownerID string, granteeID string, enabled bool, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			scope, status, activated_at, created_by, created_at, updated_at
		) VALUES ($1, 'group', $2, $3, $4, 'use', 'active', $5, $4, $5, $5)
	`, authorizationID, groupID, ownerID, granteeID, now)
	if err != nil {
		t.Fatalf("insert route smoke group authorization: %v", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.group_authorization_settings (
			authorization_id, system_account_id, group_id, enabled, group_type, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'personal', $5, $5)
	`, authorizationID, granteeID, groupID, enabled, now)
	if err != nil {
		t.Fatalf("insert route smoke group authorization settings: %v", err)
	}
}

func setW1bGroupAuthorizationEnabled(t *testing.T, ctx context.Context, db *sql.DB, authorizationID string, enabled bool, now time.Time) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.group_authorization_settings
		SET enabled = $2, updated_at = $3
		WHERE authorization_id = $1
	`, authorizationID, enabled, now); err != nil {
		t.Fatalf("update route smoke group authorization settings: %v", err)
	}
}

func insertW1bDefaultRouteStrategy(t *testing.T, ctx context.Context, db *sql.DB, ownerID string, routeID string, bindingID string, groupID string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.route_strategies (
			id, system_account_id, name, mode, status, is_default, created_at, updated_at
		) VALUES ($1, $2, $3, 'normal', 'active', true, $4, $5)
	`, routeID, ownerID, "默认路由", now, now)
	if err != nil {
		t.Fatalf("insert default route strategy: %v", err)
	}
	insertW1bRouteStrategyBinding(t, ctx, db, ownerID, routeID, bindingID, groupID, now)
}
