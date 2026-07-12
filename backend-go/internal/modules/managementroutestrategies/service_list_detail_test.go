package managementroutestrategies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
					ConfigJSON:        stringPointer(validHybridRuntimeConfigJSON()),
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
		ActorRole:            "user",
		SystemAccountID:      "sys_other",
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

func TestServiceListAdminAllIncludesOwnerFields(t *testing.T) {
	store := &routeStrategyReadStoreStub{
		page: port.ManagementRouteStrategyListPage{
			Rows: []port.ManagementRouteStrategyListRow{{
				ID:                "route_admin_all",
				SystemAccountID:   "sys_owner",
				SystemAccountName: "所有者",
				Name:              "全部策略",
				Mode:              "normal",
				Status:            "active",
			}},
		},
		enrichment: []port.ManagementRouteStrategyListEnrichment{{
			ID:              "route_admin_all",
			SystemAccountID: "sys_owner",
		}},
	}

	result, err := NewService(store).List(context.Background(), ListInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		SystemAccountID:      "all",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.SystemAccountID != "" {
		t.Fatalf("system account id = %q, want all scope", store.listInput.SystemAccountID)
	}
	if result.Items[0].SystemAccountID != "sys_owner" || result.Items[0].SystemAccountName != "所有者" {
		t.Fatalf("admin owner fields = %+v", result.Items[0])
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

func TestServiceDetailNormalizesCompleteHybridConfigLikeNode(t *testing.T) {
	store := &routeStrategyReadStoreStub{
		detailFound: true,
		detail: port.ManagementRouteStrategyDetailRow{
			ManagementRouteStrategyListRow: port.ManagementRouteStrategyListRow{
				ID:              "route_hybrid",
				SystemAccountID: "sys_owner",
				Name:            "混合策略",
				Mode:            "hybrid_smart",
				Status:          "active",
				ConfigJSON: stringPointer(`{
					"hybridRoutingConfig": {
						"scoringGroupId": " score_group ",
						"scoringModel": " scorer-model ",
						"scoringTimeoutMs": "15000",
						"scoringCacheEnabled": false,
						"cacheAffinityEnabled": false,
						"switchMinLevelDelta": false,
						"levelRoutes": [
							{"minLevel": true, "maxLevel": 3, "targetModel": " model-a "},
							{"minLevel": 4, "maxLevel": 4, "targetModel": "disabled-model", "enabled": false},
							{"minLevel": 4, "maxLevel": "10", "targetModel": " MODEL-B "}
						],
						"qualityInspection": {
							"enabled": false,
							"scoringGroupId": " quality_group ",
							"scoringModel": " ",
							"maxTriggerLevel": true,
							"maxRetries": false
						}
					}
				}`),
			},
		},
	}

	result, err := NewService(store).Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		SystemAccountID:      "sys_owner",
		RouteStrategyID:      "route_hybrid",
	})
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	assertJSONEqual(t, result.HybridRoutingConfig, `{
		"scoringGroupId": "score_group",
		"scoringModel": "scorer-model",
		"scoringContextMode": "full_request",
		"qualityPreference": "balanced",
		"scoringTimeoutMs": 15000,
		"scoringFallbackMaxLevel": 5,
		"scoringCacheEnabled": true,
		"scoringCacheTtlSeconds": 300,
		"cacheAffinityEnabled": true,
		"affinityTtlSeconds": 900,
		"switchMinLevelDelta": 0,
		"downgradeConsecutiveLowCount": 2,
		"levelRoutes": [
			{"minLevel": 1, "maxLevel": 3, "targetModel": "model-a", "enabled": true},
			{"minLevel": 4, "maxLevel": 10, "targetModel": "MODEL-B", "enabled": true}
		],
		"qualityInspection": {
			"enabled": false,
			"scoringGroupId": "quality_group",
			"scoringModel": "scorer-model",
			"triggerMode": "risk_based",
			"maxTriggerLevel": 1,
			"maxRetries": 0,
			"failureAction": "repair_then_upgrade",
			"unavailableAction": "pass_through"
		}
	}`)
}

func TestServiceDetailForcesRegularUserSelfScopeAndOmitsOwnerFields(t *testing.T) {
	store := &routeStrategyReadStoreStub{
		detailFound: true,
		detail: port.ManagementRouteStrategyDetailRow{
			ManagementRouteStrategyListRow: port.ManagementRouteStrategyListRow{
				ID:                "route_self_detail",
				SystemAccountID:   "sys_self",
				SystemAccountName: "本人",
				Name:              "我的策略",
				Mode:              "normal",
				Status:            "active",
			},
		},
	}

	result, err := NewService(store).Detail(context.Background(), DetailInput{
		ActorSystemAccountID: " sys_self ",
		ActorRole:            "user",
		SystemAccountID:      "sys_other",
		RouteStrategyID:      " route_self_detail ",
	})
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if store.detailInput.SystemAccountID != "sys_self" {
		t.Fatalf("system account id = %q", store.detailInput.SystemAccountID)
	}
	if result.SystemAccountID != "" || result.SystemAccountName != "" {
		t.Fatalf("self owner fields = %+v", result)
	}
}

func TestParseRouteStrategyRuntimeConfigRejectsInvalidHybridConfig(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "level routes must cover continuously",
			raw: `{"hybridRoutingConfig":{
				"scoringModel":"scorer",
				"levelRoutes":[
					{"minLevel":1,"maxLevel":3,"targetModel":"model-a"},
					{"minLevel":5,"maxLevel":10,"targetModel":"model-b"}
				]
			}}`,
		},
		{
			name: "level route enabled must be boolean",
			raw: `{"hybridRoutingConfig":{
				"scoringModel":"scorer",
				"levelRoutes":[
					{"minLevel":1,"maxLevel":3,"targetModel":"model-a","enabled":1},
					{"minLevel":4,"maxLevel":10,"targetModel":"model-b"}
				]
			}}`,
		},
		{
			name: "quality inspection enabled must be boolean",
			raw: `{"hybridRoutingConfig":{
				"scoringModel":"scorer",
				"levelRoutes":[
					{"minLevel":1,"maxLevel":3,"targetModel":"model-a"},
					{"minLevel":4,"maxLevel":10,"targetModel":"model-b"}
				],
				"qualityInspection":{"enabled":0}
			}}`,
		},
		{
			name: "scoring timeout must be in range",
			raw: `{"hybridRoutingConfig":{
				"scoringModel":"scorer",
				"scoringTimeoutMs":999,
				"levelRoutes":[
					{"minLevel":1,"maxLevel":3,"targetModel":"model-a"},
					{"minLevel":4,"maxLevel":10,"targetModel":"model-b"}
				]
			}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseRouteStrategyRuntimeConfig(stringPointer(tt.raw)); err == nil {
				t.Fatal("parseRouteStrategyRuntimeConfig() error = nil")
			}
		})
	}
}

func TestParseRouteStrategyRuntimeConfigUsesNodeTopLevelTruthiness(t *testing.T) {
	for _, raw := range []string{
		`{"normalRoutingConfig":false}`,
		`{"normalRoutingConfig":0}`,
	} {
		config, err := parseRouteStrategyRuntimeConfig(stringPointer(raw))
		if err != nil {
			t.Fatalf("parseRouteStrategyRuntimeConfig(%s) error = %v", raw, err)
		}
		if config.NormalRoutingConfig != nil {
			t.Fatalf("parseRouteStrategyRuntimeConfig(%s) normal config = %+v", raw, config.NormalRoutingConfig)
		}
	}
}

func TestNormalizeManagementNormalRoutingConfigDefaultsMissingValues(t *testing.T) {
	for _, value := range []any{nil, ""} {
		config, err := normalizeManagementNormalRoutingConfig(value)
		if err != nil {
			t.Fatalf("normalizeManagementNormalRoutingConfig(%#v) error = %v", value, err)
		}
		if config.SchedulingPreference != defaultSchedulingPreference || config.SpeedFirstConfig != nil {
			t.Fatalf("normalizeManagementNormalRoutingConfig(%#v) = %+v", value, config)
		}
	}
}

func TestNormalizeManagementNormalRoutingConfigRejectsFalsyPresentFields(t *testing.T) {
	tests := []string{
		`{"normalRoutingConfig":{"schedulingPreference":false}}`,
		`{"normalRoutingConfig":{"schedulingPreference":0}}`,
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":false}}`,
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":{"slowTriggerCount":0}}}`,
		`{"normalRoutingConfig":{"schedulingPreference":"cost_first","speedFirstConfig":{"slowTriggerCount":999}}}`,
	}
	for _, raw := range tests {
		if _, err := parseRouteStrategyRuntimeConfig(stringPointer(raw)); err == nil {
			t.Fatalf("parseRouteStrategyRuntimeConfig(%s) error = nil", raw)
		}
	}
}

func TestRouteStrategyConfigIntegerMatchesNodeNumberSemantics(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		fallback  int
		minValue  int
		maxValue  int
		want      int
		wantError bool
	}{
		{name: "true is one", value: true, fallback: 2, minValue: 1, maxValue: 3, want: 1},
		{name: "false is zero", value: false, fallback: 2, minValue: 0, maxValue: 3, want: 0},
		{name: "empty array is zero", value: []any{}, fallback: 2, minValue: 0, maxValue: 3, want: 0},
		{name: "single number array", value: []any{json.Number("2")}, fallback: 1, minValue: 0, maxValue: 3, want: 2},
		{name: "hex string", value: "0x10", fallback: 1, minValue: 0, maxValue: 16, want: 16},
		{name: "u0085 is not numeric whitespace", value: "\u0085", fallback: 1, minValue: 0, maxValue: 3, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := routeStrategyConfigInteger(
				tt.value,
				tt.fallback,
				tt.minValue,
				tt.maxValue,
				"invalid",
			)
			if tt.wantError {
				if err == nil {
					t.Fatalf("routeStrategyConfigInteger() = %d, want error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("routeStrategyConfigInteger() = (%d, %v), want (%d, nil)", got, err, tt.want)
			}
		})
	}
}

func TestRouteStrategyListOffsetUsesPageMinusOneAtOverflowBoundary(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	const pageSize = 2
	lastSafePage := maxInt/pageSize + 1
	if got, want := routeStrategyListOffset(lastSafePage, pageSize), (lastSafePage-1)*pageSize; got != want {
		t.Fatalf("routeStrategyListOffset(last safe page) = %d, want %d", got, want)
	}
	if got, want := routeStrategyListOffset(lastSafePage+1, pageSize), maxInt-pageSize; got != want {
		t.Fatalf("routeStrategyListOffset(overflow page) = %d, want %d", got, want)
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

	whitespaceStore := &routeStrategyReadStoreStub{
		detailFound: true,
		detail: port.ManagementRouteStrategyDetailRow{
			ManagementRouteStrategyListRow: port.ManagementRouteStrategyListRow{
				ID:         "route_whitespace",
				Mode:       "normal",
				Status:     "active",
				ConfigJSON: stringPointer(" \t\r\n "),
			},
		},
	}
	if _, err := NewService(whitespaceStore).Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_self",
		SelfOnly:             true,
		RouteStrategyID:      "route_whitespace",
	}); err == nil {
		t.Fatal("Detail() error = nil, want whitespace JSON error")
	}
}

func validHybridRuntimeConfigJSON() string {
	return `{"hybridRoutingConfig":{
		"scoringModel":"gpt-5",
		"levelRoutes":[
			{"minLevel":1,"maxLevel":3,"targetModel":"gpt-5-mini"},
			{"minLevel":4,"maxLevel":10,"targetModel":"gpt-5"}
		]
	}}`
}

func assertJSONEqual(t *testing.T, got any, wantJSON string) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(got) error = %v", err)
	}
	var gotValue any
	if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatalf("json.Unmarshal(got) error = %v", err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(wantJSON), &wantValue); err != nil {
		t.Fatalf("json.Unmarshal(want) error = %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", gotJSON, wantJSON)
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
