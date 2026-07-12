package managementroutestrategies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListTrimsLookaheadBeforeEnrichment(t *testing.T) {
	store := &routeStrategyReadStoreStub{
		page: port.ManagementRouteStrategyListPage{
			Rows: []port.ManagementRouteStrategyListRow{
				{
					ID:                "route_1",
					SystemAccountID:   "sys_1",
					SystemAccountName: "用户一",
					Name:              "默认策略",
					Mode:              "normal",
					Status:            "active",
					IsDefault:         true,
					CreatedAt:         time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC),
					UpdatedAt:         time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC),
				},
				{
					ID:                "route_2",
					SystemAccountID:   "sys_2",
					SystemAccountName: "用户二",
					Name:              "混合策略",
					Mode:              "hybrid_smart",
					Status:            "disabled",
					ConfigJSON:        stringPointer(`{"hybridRoutingConfig":{"scoringModel":"gpt-5"}}`),
					CreatedAt:         time.Date(2026, 7, 9, 1, 2, 3, 0, time.UTC),
					UpdatedAt:         time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC),
				},
				{ID: "route_lookahead", SystemAccountID: "sys_3"},
			},
		},
		enrichment: []port.ManagementRouteStrategyListEnrichment{
			{
				ID:              "route_1",
				SystemAccountID: "sys_1",
				BindingCount:    4,
				APIKeyCount:     2,
				GroupBindingPreview: []port.ManagementRouteStrategyGroupBinding{
					{ID: "binding_1", GroupID: "group_1", GroupName: "分组一", Status: "active", GroupEnabled: true},
				},
			},
			{
				ID:              "route_2",
				SystemAccountID: "sys_2",
				BindingCount:    1,
				APIKeyCount:     3,
			},
		},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		Page:                 999,
		PageSize:             2,
		PageSizeProvided:     true,
		Keyword:              " 策略 ",
		Mode:                 "invalid",
		Status:               "all",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if store.listInput.Limit != 3 || store.listInput.Offset != 1996 || store.listInput.Keyword != "策略" {
		t.Fatalf("list input = %+v", store.listInput)
	}
	if store.listInput.Mode != "" || store.listInput.Status != "" || store.listInput.SystemAccountID != "" {
		t.Fatalf("list filters = %+v", store.listInput)
	}
	if len(store.enrichmentScopes) != 2 ||
		store.enrichmentScopes[0].ID != "route_1" ||
		store.enrichmentScopes[1].ID != "route_2" {
		t.Fatalf("enrichment scopes = %#v", store.enrichmentScopes)
	}
	if len(result.Items) != 2 || !result.HasMore || result.Total != 1999 || result.Page != 999 || result.PageSize != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].SystemAccountID != "sys_1" || result.Items[0].SystemAccountName != "用户一" {
		t.Fatalf("admin owner fields = %+v", result.Items[0])
	}
	if result.Items[0].NormalRoutingConfig == nil ||
		result.Items[0].NormalRoutingConfig.SchedulingPreference != "cost_first" {
		t.Fatalf("normal config = %+v", result.Items[0].NormalRoutingConfig)
	}
	if result.Items[0].BindingCount != 4 || result.Items[0].APIKeyCount != 2 ||
		len(result.Items[0].GroupBindingPreview) != 1 {
		t.Fatalf("list enrichment = %+v", result.Items[0])
	}
	encoded, err := json.Marshal(result.Items[1])
	if err != nil {
		t.Fatalf("Marshal(list item) error = %v", err)
	}
	if bytes.Contains(encoded, []byte(`"hybridRoutingConfig"`)) {
		t.Fatalf("list must omit hybrid config: %s", encoded)
	}
}

func TestServiceListForcesSelfScopeAndOmitsOwnerFields(t *testing.T) {
	store := &routeStrategyReadStoreStub{
		page: port.ManagementRouteStrategyListPage{
			Rows: []port.ManagementRouteStrategyListRow{{
				ID:                "route_self",
				SystemAccountID:   "sys_self",
				SystemAccountName: "本人",
				Name:              "我的策略",
				Mode:              "normal",
				Status:            "active",
			}},
		},
		enrichment: []port.ManagementRouteStrategyListEnrichment{{
			ID:              "route_self",
			SystemAccountID: "sys_self",
		}},
	}

	result, err := NewService(store).List(context.Background(), ListInput{
		ActorSystemAccountID: " sys_self ",
		ActorRole:            "admin",
		SystemAccountID:      "sys_other",
		SelfOnly:             true,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.SystemAccountID != "sys_self" {
		t.Fatalf("system account id = %q", store.listInput.SystemAccountID)
	}
	if result.Items[0].SystemAccountID != "" || result.Items[0].SystemAccountName != "" {
		t.Fatalf("self owner fields = %+v", result.Items[0])
	}
}

func TestServiceDetailReturnsFullBindingsAndNormalizesSpeedConfig(t *testing.T) {
	store := &routeStrategyReadStoreStub{
		detailFound: true,
		detail: port.ManagementRouteStrategyDetailRow{
			ManagementRouteStrategyListRow: port.ManagementRouteStrategyListRow{
				ID:                "route_speed",
				SystemAccountID:   "sys_owner",
				SystemAccountName: "所有者",
				Name:              "速度优先",
				Mode:              "normal",
				Status:            "active",
				ConfigJSON: stringPointer(`{
					"normalRoutingConfig": {
						"schedulingPreference": "speed_first",
						"speedFirstConfig": {
							"firstByteThresholdMs": "30000",
							"slowTriggerCount": 4
						}
					}
				}`),
				CreatedAt: time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC),
				UpdatedAt: time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC),
			},
			GroupBindings: []port.ManagementRouteStrategyGroupBinding{
				{
					ID:           "binding_1",
					GroupID:      "group_1",
					GroupName:    "分组一",
					ProviderCode: "openai",
					Priority:     1,
					Weight:       80,
					Status:       "active",
					GroupEnabled: true,
				},
			},
			APIKeyCount: 7,
		},
	}

	result, err := NewService(store).Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "super_admin",
		SystemAccountID:      "sys_owner",
		RouteStrategyID:      " route_speed ",
	})
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if store.detailInput.RouteStrategyID != "route_speed" || store.detailInput.SystemAccountID != "sys_owner" {
		t.Fatalf("detail input = %+v", store.detailInput)
	}
	if result.NormalRoutingConfig == nil || result.NormalRoutingConfig.SpeedFirstConfig == nil {
		t.Fatalf("normal config = %+v", result.NormalRoutingConfig)
	}
	speed := result.NormalRoutingConfig.SpeedFirstConfig
	if speed.FirstByteThresholdMs != 30000 || speed.SlowTriggerCount != 4 ||
		speed.SlowWindowSeconds != 120 || speed.MaxFirstByteRetriesPerRequest != 2 {
		t.Fatalf("speed config = %+v", speed)
	}
	if len(result.GroupBindings) != 1 || result.APIKeyCount != 7 {
		t.Fatalf("detail = %+v", result)
	}
}

func TestRouteStrategyReadScopeDoesNotBroadenNonECMAScriptWhitespaceOwner(t *testing.T) {
	const owner = "\u0085"
	systemAccountID, includeOwner, err := routeStrategyReadScope("sys_admin", "admin", owner, false)
	if err != nil {
		t.Fatalf("routeStrategyReadScope() error = %v", err)
	}
	if systemAccountID != owner || !includeOwner {
		t.Fatalf("scope = (%q, %v), want (%q, true)", systemAccountID, includeOwner, owner)
	}
}

func TestServiceDetailReturnsNotFoundAndRejectsDamagedConfig(t *testing.T) {
	notFoundStore := &routeStrategyReadStoreStub{}
	_, err := NewService(notFoundStore).Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_self",
		SelfOnly:             true,
		RouteStrategyID:      "route_missing",
	})
	if !errors.Is(err, ErrRouteStrategyNotFound) {
		t.Fatalf("Detail() error = %v, want ErrRouteStrategyNotFound", err)
	}

	damagedStore := &routeStrategyReadStoreStub{
		detailFound: true,
		detail: port.ManagementRouteStrategyDetailRow{
			ManagementRouteStrategyListRow: port.ManagementRouteStrategyListRow{
				ID:         "route_broken",
				Mode:       "normal",
				Status:     "active",
				ConfigJSON: stringPointer(`{"normalRoutingConfig":[]}`),
			},
		},
	}
	if _, err := NewService(damagedStore).Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_self",
		SelfOnly:             true,
		RouteStrategyID:      "route_broken",
	}); err == nil {
		t.Fatal("Detail() error = nil, want damaged config error")
	}

	trailingStore := &routeStrategyReadStoreStub{
		detailFound: true,
		detail: port.ManagementRouteStrategyDetailRow{
			ManagementRouteStrategyListRow: port.ManagementRouteStrategyListRow{
				ID:         "route_trailing",
				Mode:       "normal",
				Status:     "active",
				ConfigJSON: stringPointer(`{} {}`),
			},
		},
	}
	if _, err := NewService(trailingStore).Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_self",
		SelfOnly:             true,
		RouteStrategyID:      "route_trailing",
	}); err == nil {
		t.Fatal("Detail() error = nil, want trailing JSON error")
	}
}

func stringPointer(value string) *string {
	return &value
}

type routeStrategyReadStoreStub struct {
	routeStrategyOptionStoreStub
	listInput        port.ManagementRouteStrategyListInput
	page             port.ManagementRouteStrategyListPage
	enrichmentScopes []port.ManagementRouteStrategyScope
	enrichment       []port.ManagementRouteStrategyListEnrichment
	detailInput      port.ManagementRouteStrategyDetailInput
	detail           port.ManagementRouteStrategyDetailRow
	detailFound      bool
	err              error
}

func (s *routeStrategyReadStoreStub) ListManagementRouteStrategies(
	_ context.Context,
	input port.ManagementRouteStrategyListInput,
) (port.ManagementRouteStrategyListPage, error) {
	s.listInput = input
	return s.page, s.err
}

func (s *routeStrategyReadStoreStub) ListManagementRouteStrategyListEnrichment(
	_ context.Context,
	scopes []port.ManagementRouteStrategyScope,
) ([]port.ManagementRouteStrategyListEnrichment, error) {
	s.enrichmentScopes = append([]port.ManagementRouteStrategyScope(nil), scopes...)
	return s.enrichment, s.err
}

func (s *routeStrategyReadStoreStub) FindManagementRouteStrategyDetail(
	_ context.Context,
	input port.ManagementRouteStrategyDetailInput,
) (port.ManagementRouteStrategyDetailRow, bool, error) {
	s.detailInput = input
	return s.detail, s.detailFound, s.err
}
