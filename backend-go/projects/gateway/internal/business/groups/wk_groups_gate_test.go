package groups

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

func TestOwnerGateBlocksEveryMutation(t *testing.T) {
	db := testDB(t)
	blocked := &Service{db: db, mode: modelcheckauth.SQLite, gate: OwnerGate{}, cipher: testCipher{}}
	ctx := context.Background()
	actor := Actor{SystemAccountID: "sys-1", Role: "admin"}
	if _, err := blocked.CreateGroup(ctx, actor, GroupInput{Name: "g", ProviderCode: "openai"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CreateGroup err=%v", err)
	}
	if _, err := blocked.FindGroup(ctx, actor, "", "g"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("FindGroup err=%v", err)
	}
	if _, err := blocked.ListGroups(ctx, actor, "", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("ListGroups err=%v", err)
	}
	if _, err := blocked.UpdateGroup(ctx, actor, "g", "r", GroupInput{Name: "g", ProviderCode: "openai"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("UpdateGroup err=%v", err)
	}
	if err := blocked.DeleteGroup(ctx, actor, "g", "r"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("DeleteGroup err=%v", err)
	}
	if _, err := blocked.CreateRouteStrategy(ctx, actor, RouteStrategyInput{Name: "r"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CreateRouteStrategy err=%v", err)
	}
	if _, err := blocked.FindRouteStrategy(ctx, actor, "", "r"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("FindRouteStrategy err=%v", err)
	}
	if _, err := blocked.ListRouteStrategies(ctx, actor, "", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("ListRouteStrategies err=%v", err)
	}
	if _, err := blocked.UpdateRouteStrategy(ctx, actor, "r", "rev", RouteStrategyInput{Name: "r"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("UpdateRouteStrategy err=%v", err)
	}
	if err := blocked.DeleteRouteStrategy(ctx, actor, "r", "rev"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("DeleteRouteStrategy err=%v", err)
	}
	if _, _, err := blocked.CreateAPIKey(ctx, actor, APIKeyInput{Name: "k"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CreateAPIKey err=%v", err)
	}
	if _, err := blocked.FindAPIKey(ctx, actor, "", "k"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("FindAPIKey err=%v", err)
	}
	if _, err := blocked.ListAPIKeys(ctx, actor, "", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("ListAPIKeys err=%v", err)
	}
	if _, err := blocked.UpdateAPIKey(ctx, actor, "k", "rev", APIKeyInput{Name: "k"}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("UpdateAPIKey err=%v", err)
	}
	if _, _, err := blocked.RotateAPIKeySecret(ctx, actor, "k", "rev", "sk"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("RotateAPIKeySecret err=%v", err)
	}
	if err := blocked.DeleteAPIKey(ctx, actor, "k", "rev"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("DeleteAPIKey err=%v", err)
	}
	// nil 服务与 nil db 同样拒绝。
	var nilSvc *Service
	if err := nilSvc.DeleteGroup(ctx, actor, "g", "r"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil service err=%v", err)
	}
	noDB := &Service{mode: modelcheckauth.SQLite, gate: OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, cipher: testCipher{}}
	if err := noDB.DeleteGroup(ctx, actor, "g", "r"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil db err=%v", err)
	}
}

func TestSQLFailurePathsFailClosed(t *testing.T) {
	scenarios := []struct {
		name  string
		drop  string
		exact func(t *testing.T, svc *Service, ctx context.Context, admin Actor)
	}{
		{
			name: "groups relation missing",
			drop: "groups",
			exact: func(t *testing.T, svc *Service, ctx context.Context, admin Actor) {
				if _, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: "openai"}); err == nil {
					t.Fatal("CreateGroup 必须失败")
				}
				if _, err := svc.FindGroup(ctx, admin, "", "g"); err == nil {
					t.Fatal("FindGroup 必须失败")
				}
				if _, err := svc.ListGroups(ctx, admin, "", 1); err == nil {
					t.Fatal("ListGroups 必须失败")
				}
				if _, err := svc.UpdateGroup(ctx, admin, "g", "r", GroupInput{Name: "g", ProviderCode: "openai"}); err == nil {
					t.Fatal("UpdateGroup 必须失败")
				}
				if err := svc.DeleteGroup(ctx, admin, "g", "r"); err == nil {
					t.Fatal("DeleteGroup 必须失败")
				}
			},
		},
		{
			name: "route_strategies relation missing",
			drop: "route_strategies",
			exact: func(t *testing.T, svc *Service, ctx context.Context, admin Actor) {
				group, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: "openai"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: group.ID}}}); err == nil {
					t.Fatal("CreateRouteStrategy 必须失败")
				}
				if _, err := svc.FindRouteStrategy(ctx, admin, "", "r"); err == nil {
					t.Fatal("FindRouteStrategy 必须失败")
				}
				if _, err := svc.ListRouteStrategies(ctx, admin, "", 1); err == nil {
					t.Fatal("ListRouteStrategies 必须失败")
				}
				if _, err := svc.UpdateRouteStrategy(ctx, admin, "r", "rev", RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: group.ID}}}); err == nil {
					t.Fatal("UpdateRouteStrategy 必须失败")
				}
				if err := svc.DeleteRouteStrategy(ctx, admin, "r", "rev"); err == nil {
					t.Fatal("DeleteRouteStrategy 必须失败")
				}
				if _, _, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: "r", Name: "k", Secret: "sk"}); err == nil {
					t.Fatal("CreateAPIKey 必须失败")
				}
				if _, err := svc.UpdateAPIKey(ctx, admin, "k", "rev", APIKeyInput{RouteStrategyID: "r", Name: "k"}); err == nil {
					t.Fatal("UpdateAPIKey 必须失败")
				}
			},
		},
		{
			name: "route_strategy_groups relation missing",
			drop: "route_strategy_groups",
			exact: func(t *testing.T, svc *Service, ctx context.Context, admin Actor) {
				group, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: "openai"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: group.ID}}}); err == nil {
					t.Fatal("CreateRouteStrategy 必须失败")
				}
				if _, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r2", Mode: "normal"}); err == nil {
					t.Fatal("缺关系时零绑定策略不应创建成功")
				}
				if _, err := svc.FindRouteStrategy(ctx, admin, "", "anything"); err == nil {
					t.Fatal("FindRouteStrategy 必须失败")
				}
				if err := svc.DeleteGroup(ctx, admin, group.ID, group.Revision); err == nil {
					t.Fatal("DeleteGroup 引用检查必须失败")
				}
			},
		},
		{
			name: "api_keys relation missing",
			drop: "api_keys",
			exact: func(t *testing.T, svc *Service, ctx context.Context, admin Actor) {
				group, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: "openai"})
				if err != nil {
					t.Fatal(err)
				}
				strategy, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: group.ID}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k", Secret: "sk"}); err == nil {
					t.Fatal("CreateAPIKey 必须失败")
				}
				if _, err := svc.FindAPIKey(ctx, admin, "", "k"); err == nil {
					t.Fatal("FindAPIKey 必须失败")
				}
				if _, err := svc.ListAPIKeys(ctx, admin, "", 1); err == nil {
					t.Fatal("ListAPIKeys 必须失败")
				}
				if _, err := svc.UpdateAPIKey(ctx, admin, "k", "rev", APIKeyInput{RouteStrategyID: strategy.ID, Name: "k"}); err == nil {
					t.Fatal("UpdateAPIKey 必须失败")
				}
				if _, _, err := svc.RotateAPIKeySecret(ctx, admin, "k", "rev", "sk"); err == nil {
					t.Fatal("RotateAPIKeySecret 必须失败")
				}
				if err := svc.DeleteAPIKey(ctx, admin, "k", "rev"); err == nil {
					t.Fatal("DeleteAPIKey 必须失败")
				}
				if err := svc.DeleteRouteStrategy(ctx, admin, strategy.ID, strategy.Revision); err == nil {
					t.Fatal("DeleteRouteStrategy 引用检查必须失败")
				}
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			db := testDB(t)
			if _, err := db.Exec("DROP TABLE " + scenario.drop); err != nil {
				t.Fatal(err)
			}
			svc := wkReadyService(t, db, testCipher{})
			scenario.exact(t, svc, context.Background(), Actor{SystemAccountID: "sys-1", Role: "admin"})
		})
	}
}

func TestCanceledContextFailsClosed(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: group.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k", Secret: "sk"}); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.CreateGroup(canceled, admin, GroupInput{Name: "g2", ProviderCode: "openai"}); err == nil {
		t.Fatal("CreateGroup 必须失败")
	}
	if _, err := svc.FindGroup(canceled, admin, "", "g"); err == nil {
		t.Fatal("FindGroup 必须失败")
	}
	if _, err := svc.ListGroups(canceled, admin, "", 1); err == nil {
		t.Fatal("ListGroups 必须失败")
	}
	if _, err := svc.UpdateGroup(canceled, admin, "g", "r", GroupInput{Name: "g", ProviderCode: "openai"}); err == nil {
		t.Fatal("UpdateGroup 必须失败")
	}
	if err := svc.DeleteGroup(canceled, admin, "g", "r"); err == nil {
		t.Fatal("DeleteGroup 必须失败")
	}
	if _, err := svc.CreateRouteStrategy(canceled, admin, RouteStrategyInput{Name: "r2", Bindings: []RouteBinding{{GroupID: "g"}}}); err == nil {
		t.Fatal("CreateRouteStrategy 必须失败")
	}
	if _, err := svc.FindRouteStrategy(canceled, admin, "", strategy.ID); err == nil {
		t.Fatal("FindRouteStrategy 必须失败")
	}
	if _, err := svc.ListRouteStrategies(canceled, admin, "", 1); err == nil {
		t.Fatal("ListRouteStrategies 必须失败")
	}
	if _, err := svc.UpdateRouteStrategy(canceled, admin, strategy.ID, strategy.Revision, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: group.ID}}}); err == nil {
		t.Fatal("UpdateRouteStrategy 必须失败")
	}
	if err := svc.DeleteRouteStrategy(canceled, admin, strategy.ID, strategy.Revision); err == nil {
		t.Fatal("DeleteRouteStrategy 必须失败")
	}
	if _, _, err := svc.CreateAPIKey(canceled, admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k2", Secret: "sk2"}); err == nil {
		t.Fatal("CreateAPIKey 必须失败")
	}
	if _, err := svc.FindAPIKey(canceled, admin, "", "k"); err == nil {
		t.Fatal("FindAPIKey 必须失败")
	}
	if _, err := svc.ListAPIKeys(canceled, admin, "", 1); err == nil {
		t.Fatal("ListAPIKeys 必须失败")
	}
	if _, err := svc.UpdateAPIKey(canceled, admin, "k", "rev", APIKeyInput{RouteStrategyID: strategy.ID, Name: "k"}); err == nil {
		t.Fatal("UpdateAPIKey 必须失败")
	}
	if _, _, err := svc.RotateAPIKeySecret(canceled, admin, "k", "rev", "sk2"); err == nil {
		t.Fatal("RotateAPIKeySecret 必须失败")
	}
	if err := svc.DeleteAPIKey(canceled, admin, "k", "rev"); err == nil {
		t.Fatal("DeleteAPIKey 必须失败")
	}
}
