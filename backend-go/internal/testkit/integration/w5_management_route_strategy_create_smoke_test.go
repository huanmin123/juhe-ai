//go:build integration

package integration

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/modules/publicgroups"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

type w5ManagementRouteStrategyCreateInvalidator struct {
	mu      sync.Mutex
	reasons []string
}

func (i *w5ManagementRouteStrategyCreateInvalidator) InvalidateGatewayRuntime(
	_ context.Context,
	reason string,
) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason)
	return nil
}

func (i *w5ManagementRouteStrategyCreateInvalidator) snapshot() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.reasons...)
}

func TestW5ManagementRouteStrategyCreatePostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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

	now := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	var groupSequence int
	groupService := publicgroups.NewService(publicgroups.Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID: func(prefix string) string {
			groupSequence++
			return prefix + "_w5_route_create_" + strconv.Itoa(groupSequence)
		},
	})

	primary, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "w5-route-create-owner",
		TargetDisplayName: "W5 Route Create Owner",
		Name:              "Primary Group",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("create primary group: %v", err)
	}
	backup, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername: "w5-route-create-owner",
		Name:           "Backup Group",
		ProviderCode:   "gpt",
	})
	if err != nil {
		t.Fatalf("create backup group: %v", err)
	}
	authorized, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "w5-route-create-resource-owner",
		TargetDisplayName: "W5 Route Resource Owner",
		Name:              "Authorized Group",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("create authorized group: %v", err)
	}
	insertW1bGroupAuthorization(
		t,
		ctx,
		db,
		"rauth_w5_route_create",
		authorized.Group.ID,
		authorized.Target.SystemAccountID,
		primary.Target.SystemAccountID,
		true,
		now,
	)
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.system_accounts
		SET status = 'disabled', updated_at = $2
		WHERE id = $1
	`, primary.Target.SystemAccountID, now); err != nil {
		t.Fatalf("disable route strategy target: %v", err)
	}

	invalidator := &w5ManagementRouteStrategyCreateInvalidator{}
	var routeSequence int
	service := managementroutestrategies.NewServiceWithOptions(
		managementroutestrategies.ServiceOptions{
			CreateStore: store,
			Transactor:  store,
			Invalidator: invalidator,
			Now:         func() time.Time { return now },
			NewID: func(prefix string) string {
				routeSequence++
				return prefix + "_w5_route_create_" + strconv.Itoa(routeSequence)
			},
		},
	)

	tests := []struct {
		name         string
		mode         string
		includeOwner bool
		bindings     []managementroutestrategies.CreateGroupBindingInput
		hybridConfig managementroutestrategies.ConfigInput
	}{
		{
			name: "Normal Authorized",
			bindings: []managementroutestrategies.CreateGroupBindingInput{
				{GroupID: authorized.Group.ID},
			},
		},
		{
			name:         "Weighted",
			mode:         "weighted",
			includeOwner: true,
			bindings: []managementroutestrategies.CreateGroupBindingInput{
				{GroupID: primary.Group.ID, Priority: 1, Weight: 70},
				{GroupID: backup.Group.ID, Priority: 2, Weight: 30},
			},
		},
		{
			name: "Failover",
			mode: "failover",
			bindings: []managementroutestrategies.CreateGroupBindingInput{
				{GroupID: backup.Group.ID, Priority: 2},
				{GroupID: primary.Group.ID, Priority: 1},
			},
		},
		{
			name: "Round Robin",
			mode: "round_robin",
			bindings: []managementroutestrategies.CreateGroupBindingInput{
				{GroupID: primary.Group.ID, Priority: 1},
				{GroupID: backup.Group.ID, Priority: 2},
			},
		},
		{
			name: "Hybrid",
			mode: "hybrid_smart",
			bindings: []managementroutestrategies.CreateGroupBindingInput{
				{GroupID: primary.Group.ID},
			},
			hybridConfig: managementroutestrategies.NewConfigInput(map[string]any{
				"scoringGroupId":    primary.Group.ID,
				"scoringModel":      "gpt-5.6-sol",
				"qualityPreference": "quality_first",
				"levelRoutes": []any{
					map[string]any{
						"minLevel":    1,
						"maxLevel":    5,
						"targetModel": "gpt-5.6-terra",
					},
					map[string]any{
						"minLevel":    6,
						"maxLevel":    10,
						"targetModel": "gpt-5.6-sol",
					},
				},
			}, true),
		},
	}

	for _, test := range tests {
		result, err := service.Create(ctx, managementroutestrategies.CreateInput{
			SystemAccountID:            primary.Target.SystemAccountID,
			IncludeSystemAccountFields: test.includeOwner,
			Name:                       test.name,
			Mode:                       test.mode,
			GroupBindings:              test.bindings,
			HybridRoutingConfig:        test.hybridConfig,
		})
		if err != nil {
			t.Fatalf("create %s route strategy: %v", test.mode, err)
		}
		if result.Mode != defaultW5RouteCreateMode(test.mode) ||
			result.Status != "active" ||
			len(result.GroupBindings) != len(test.bindings) {
			t.Fatalf("created %s result = %+v", test.name, result)
		}
		if test.includeOwner {
			if result.SystemAccountID != primary.Target.SystemAccountID ||
				result.SystemAccountName != "W5 Route Create Owner" {
				t.Fatalf("admin owner fields = (%q, %q)", result.SystemAccountID, result.SystemAccountName)
			}
		} else if result.SystemAccountID != "" || result.SystemAccountName != "" {
			t.Fatalf("self result leaked owner fields: %+v", result)
		}
		if test.name == "Normal Authorized" &&
			(result.GroupBindings[0].GroupID != authorized.Group.ID ||
				result.GroupBindings[0].GroupName != "Authorized Group") {
			t.Fatalf("authorized binding = %+v", result.GroupBindings[0])
		}
	}

	_, err = service.Create(ctx, managementroutestrategies.CreateInput{
		SystemAccountID: primary.Target.SystemAccountID,
		Name:            "Normal Authorized",
		GroupBindings: []managementroutestrategies.CreateGroupBindingInput{
			{GroupID: primary.Group.ID},
		},
	})
	var nameExists *managementroutestrategies.NameExistsError
	if !errors.As(err, &nameExists) {
		t.Fatalf("duplicate create error = %T %v, want NameExistsError", err, err)
	}

	var routeCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.route_strategies
		WHERE system_account_id = $1
		  AND name IN ('Normal Authorized', 'Weighted', 'Failover', 'Round Robin', 'Hybrid')
	`, primary.Target.SystemAccountID).Scan(&routeCount); err != nil {
		t.Fatalf("count persisted route strategies: %v", err)
	}
	if routeCount != len(tests) {
		t.Fatalf("persisted route strategies = %d, want %d", routeCount, len(tests))
	}

	var bindingCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.route_strategy_group_bindings AS bindings
		JOIN juhe_business.route_strategies AS strategies
		  ON strategies.id = bindings.route_strategy_id
		WHERE strategies.system_account_id = $1
		  AND strategies.name IN ('Normal Authorized', 'Weighted', 'Failover', 'Round Robin', 'Hybrid')
	`, primary.Target.SystemAccountID).Scan(&bindingCount); err != nil {
		t.Fatalf("count persisted route strategy bindings: %v", err)
	}
	if bindingCount != 8 {
		t.Fatalf("persisted route strategy bindings = %d, want 8", bindingCount)
	}

	reasons := invalidator.snapshot()
	if len(reasons) != len(tests) {
		t.Fatalf("runtime invalidations = %#v, want %d", reasons, len(tests))
	}
	for index, reason := range reasons {
		if reason != managementroutestrategies.RouteStrategyCreatedReason {
			t.Fatalf("runtime invalidation[%d] = %q", index, reason)
		}
	}
}

func defaultW5RouteCreateMode(mode string) string {
	if mode == "" {
		return "normal"
	}
	return mode
}
