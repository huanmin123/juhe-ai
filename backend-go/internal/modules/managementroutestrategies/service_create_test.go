package managementroutestrategies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var managementRouteStrategyCreateTestNow = time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC)

func TestServiceCreateSupportsFiveModes(t *testing.T) {
	tests := []struct {
		name           string
		mode           string
		bindings       []CreateGroupBindingInput
		normalConfig   ConfigInput
		hybridConfig   ConfigInput
		wantConfig     bool
		wantNormal     bool
		wantHybrid     bool
		wantGroupIDs   []string
		wantPriorities []int
	}{
		{
			name:     "normal defaults to cost first without stored config",
			bindings: []CreateGroupBindingInput{{GroupID: " group_1 "}},
			normalConfig: NewConfigInput(map[string]any{
				"schedulingPreference": "cost_first",
				"speedFirstConfig": map[string]any{
					"slowTriggerCount": 4,
				},
			}, true),
			wantNormal:     true,
			wantGroupIDs:   []string{"group_1"},
			wantPriorities: []int{1},
		},
		{
			name: "weighted accepts one active binding without extra weight rules",
			mode: "weighted",
			bindings: []CreateGroupBindingInput{{
				GroupID: "group_1",
				Weight:  37,
			}},
			wantGroupIDs:   []string{"group_1"},
			wantPriorities: []int{1},
		},
		{
			name: "round robin accepts one active binding",
			mode: "round_robin",
			bindings: []CreateGroupBindingInput{{
				GroupID: "group_2",
			}},
			wantGroupIDs:   []string{"group_2"},
			wantPriorities: []int{1},
		},
		{
			name: "failover sorts primary and backup by priority",
			mode: "failover",
			bindings: []CreateGroupBindingInput{
				{GroupID: "group_2", Priority: 2},
				{GroupID: "group_1", Priority: 1},
			},
			wantGroupIDs:   []string{"group_1", "group_2"},
			wantPriorities: []int{1, 2},
		},
		{
			name: "hybrid stores complete normalized config",
			mode: "hybrid_smart",
			bindings: []CreateGroupBindingInput{{
				GroupID: "group_1",
			}},
			hybridConfig:   NewConfigInput(validManagementHybridCreateConfig(), true),
			wantConfig:     true,
			wantHybrid:     true,
			wantGroupIDs:   []string{"group_1"},
			wantPriorities: []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyCreateStore()
			store.target = port.PublicGroupTarget{
				ID:          "sys_owner",
				DisplayName: "停用所有者",
				Status:      "disabled",
			}
			store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
				ID:              "group_1",
				SystemAccountID: "sys_owner",
				Name:            "分组一",
				ProviderCode:    "openai",
				Enabled:         true,
			}
			store.groups["group_2"] = port.PublicRouteStrategyBindableGroup{
				ID:              "group_2",
				SystemAccountID: "sys_owner",
				Name:            "分组二",
				ProviderCode:    "openai",
				Enabled:         true,
			}
			service, tx, invalidator, _ := newManagementRouteStrategyCreateService(store)

			result, err := service.Create(context.Background(), CreateInput{
				SystemAccountID:            " sys_owner ",
				IncludeSystemAccountFields: true,
				Name:                       "  策略名称  ",
				Description:                stringPointer("  策略说明  "),
				Mode:                       tt.mode,
				GroupBindings:              tt.bindings,
				NormalRoutingConfig:        tt.normalConfig,
				HybridRoutingConfig:        tt.hybridConfig,
			})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if tx.calls != 1 {
				t.Fatalf("transaction calls = %d, want 1", tx.calls)
			}
			if len(store.createInputs) != 1 {
				t.Fatalf("create inputs = %d, want 1", len(store.createInputs))
			}
			input := store.createInputs[0]
			wantMode := tt.mode
			if wantMode == "" {
				wantMode = "normal"
			}
			if input.SystemAccountID != "sys_owner" ||
				input.Name != "策略名称" ||
				input.Description == nil ||
				*input.Description != "策略说明" ||
				string(input.Mode) != wantMode ||
				input.Status != port.PublicRouteStrategyStatusActive {
				t.Fatalf("create input = %+v", input)
			}
			if (input.ConfigJSON != nil) != tt.wantConfig {
				t.Fatalf("ConfigJSON = %v, want present %v", input.ConfigJSON, tt.wantConfig)
			}
			if len(input.Bindings) != len(tt.wantGroupIDs) {
				t.Fatalf("bindings = %+v", input.Bindings)
			}
			for index, binding := range input.Bindings {
				if binding.GroupID != tt.wantGroupIDs[index] ||
					binding.Priority != tt.wantPriorities[index] ||
					binding.Status != port.PublicRouteStrategyStatusActive {
					t.Fatalf("binding[%d] = %+v", index, binding)
				}
			}
			if result.SystemAccountID != "sys_owner" || result.SystemAccountName != "停用所有者" {
				t.Fatalf("owner fields = (%q, %q)", result.SystemAccountID, result.SystemAccountName)
			}
			if result.Name != "策略名称" || result.Description == nil || *result.Description != "策略说明" {
				t.Fatalf("result = %+v", result)
			}
			if result.IsDefault || result.APIKeyCount != 0 {
				t.Fatalf("created defaults = isDefault %v apiKeyCount %d", result.IsDefault, result.APIKeyCount)
			}
			if (result.NormalRoutingConfig != nil) != tt.wantNormal {
				t.Fatalf("normal config = %+v", result.NormalRoutingConfig)
			}
			if tt.wantNormal && result.NormalRoutingConfig.SchedulingPreference != defaultSchedulingPreference {
				t.Fatalf("normal config = %+v", result.NormalRoutingConfig)
			}
			if (result.HybridRoutingConfig != nil) != tt.wantHybrid {
				t.Fatalf("hybrid config = %+v", result.HybridRoutingConfig)
			}
			if tt.wantHybrid {
				assertJSONEqual(t, result.HybridRoutingConfig, `{
					"scoringGroupId": "score_group",
					"scoringModel": "scorer-model",
					"scoringContextMode": "full_request",
					"qualityPreference": "quality_first",
					"scoringTimeoutMs": 15000,
					"scoringFallbackMaxLevel": 5,
					"scoringCacheEnabled": true,
					"scoringCacheTtlSeconds": 300,
					"cacheAffinityEnabled": true,
					"affinityTtlSeconds": 900,
					"switchMinLevelDelta": 2,
					"downgradeConsecutiveLowCount": 2,
					"levelRoutes": [
						{"minLevel": 1, "maxLevel": 3, "targetModel": "model-a", "enabled": true},
						{"minLevel": 4, "maxLevel": 10, "targetModel": "model-b", "enabled": true}
					],
					"qualityInspection": {
						"enabled": true,
						"scoringModel": "quality-model",
						"triggerMode": "risk_based",
						"maxTriggerLevel": 6,
						"maxRetries": 2,
						"failureAction": "repair_then_upgrade",
						"unavailableAction": "pass_through"
					}
				}`)
				stored, err := parseRouteStrategyRuntimeConfig(input.ConfigJSON)
				if err != nil {
					t.Fatalf("parse stored hybrid config error = %v", err)
				}
				assertJSONEqual(t, stored.HybridRoutingConfig, mustMarshalJSON(t, result.HybridRoutingConfig))
			}
			if invalidator.calls != 1 || invalidator.reasons[0] != RouteStrategyCreatedReason {
				t.Fatalf("invalidation = calls %d reasons %#v", invalidator.calls, invalidator.reasons)
			}
		})
	}
}

func TestServiceCreateUsesECMAScriptTrimSemantics(t *testing.T) {
	const nonECMAScriptWhitespace = "\u0085"
	store := newManagementRouteStrategyCreateStore()
	store.target = port.PublicGroupTarget{ID: "sys_owner", Status: "active"}
	store.groups[nonECMAScriptWhitespace] = port.PublicRouteStrategyBindableGroup{
		ID: nonECMAScriptWhitespace, SystemAccountID: "sys_owner", Enabled: true,
	}
	service, _, _, _ := newManagementRouteStrategyCreateService(store)

	result, err := service.Create(context.Background(), CreateInput{
		SystemAccountID: "sys_owner",
		Name:            nonECMAScriptWhitespace,
		Description:     stringPointer(nonECMAScriptWhitespace),
		GroupBindings: []CreateGroupBindingInput{{
			GroupID: nonECMAScriptWhitespace,
		}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.Name != nonECMAScriptWhitespace ||
		result.Description == nil ||
		*result.Description != nonECMAScriptWhitespace ||
		len(result.GroupBindings) != 1 ||
		result.GroupBindings[0].GroupID != nonECMAScriptWhitespace {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceCreateDistinguishesOmittedAndExplicitZeroValues(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*CreateInput)
		wantText string
	}{
		{
			name: "explicit empty mode",
			mutate: func(input *CreateInput) {
				input.ModeSet = true
			},
			wantText: "路由策略模式无效",
		},
		{
			name: "explicit empty status",
			mutate: func(input *CreateInput) {
				input.StatusSet = true
			},
			wantText: "策略路由状态无效",
		},
		{
			name: "explicit zero priority",
			mutate: func(input *CreateInput) {
				input.GroupBindings[0].PrioritySet = true
			},
			wantText: "策略路由分组优先级必须是大于 0 的整数",
		},
		{
			name: "explicit zero weight",
			mutate: func(input *CreateInput) {
				input.GroupBindings[0].WeightSet = true
			},
			wantText: "策略路由分组权重必须是 1-100",
		},
		{
			name: "explicit empty binding status",
			mutate: func(input *CreateInput) {
				input.GroupBindings[0].StatusSet = true
			},
			wantText: "策略路由分组绑定状态无效",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyCreateStore()
			store.target = port.PublicGroupTarget{ID: "sys_owner", Status: "active"}
			store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
				ID: "group_1", SystemAccountID: "sys_owner", Enabled: true,
			}
			input := CreateInput{
				SystemAccountID: "sys_owner",
				Name:            "测试策略",
				GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
			}
			tt.mutate(&input)
			service, tx, invalidator, _ := newManagementRouteStrategyCreateService(store)

			_, err := service.Create(context.Background(), input)
			message, ok := ValidationMessage(err)
			if !ok {
				t.Fatalf("Create() error = %T %v, want typed validation error", err, err)
			}
			if !strings.Contains(message, tt.wantText) {
				t.Fatalf("validation message = %q, want contains %q", message, tt.wantText)
			}
			if tx.calls != 0 || invalidator.calls != 0 {
				t.Fatalf("transaction calls = %d, invalidation calls = %d, want 0", tx.calls, invalidator.calls)
			}
		})
	}
}

func TestServiceCreateRejectsNonZodConfigTypes(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		config func() any
		normal bool
	}{
		{name: "normal top level boolean", normal: true, config: func() any { return true }},
		{name: "normal top level string", normal: true, config: func() any { return "" }},
		{name: "normal top level array", normal: true, config: func() any { return []any{} }},
		{
			name:   "normal scheduling preference boolean",
			normal: true,
			config: func() any { return map[string]any{"schedulingPreference": true} },
		},
		{
			name:   "normal speed config null",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig":     nil,
				}
			},
		},
		{
			name:   "normal speed config array",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig":     []any{},
				}
			},
		},
		{
			name:   "normal speed config boolean",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig":     true,
				}
			},
		},
		{
			name:   "normal speed config string",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig":     "config",
				}
			},
		},
		{
			name:   "normal numeric string",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig": map[string]any{
						"slowTriggerCount": "4",
					},
				}
			},
		},
		{
			name:   "normal numeric object",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig": map[string]any{
						"slowTriggerCount": map[string]any{},
					},
				}
			},
		},
		{
			name:   "normal numeric boolean",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig": map[string]any{
						"maxFirstByteRetriesPerRequest": true,
					},
				}
			},
		},
		{
			name:   "normal numeric array",
			normal: true,
			config: func() any {
				return map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig": map[string]any{
						"slowTriggerCount": []any{4},
					},
				}
			},
		},
		{name: "hybrid top level boolean", mode: "hybrid_smart", config: func() any { return true }},
		{name: "hybrid top level string", mode: "hybrid_smart", config: func() any { return "config" }},
		{name: "hybrid top level array", mode: "hybrid_smart", config: func() any { return []any{} }},
		{
			name: "hybrid optional string boolean",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["scoringGroupId"] = true
				return value
			},
		},
		{
			name: "hybrid optional boolean string",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["scoringCacheEnabled"] = "true"
				return value
			},
		},
		{
			name: "hybrid numeric string",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["scoringTimeoutMs"] = "15000"
				return value
			},
		},
		{
			name: "hybrid numeric boolean",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["switchMinLevelDelta"] = false
				return value
			},
		},
		{
			name: "hybrid level routes object",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["levelRoutes"] = map[string]any{}
				return value
			},
		},
		{
			name: "hybrid level route boolean",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["levelRoutes"] = []any{true}
				return value
			},
		},
		{
			name: "hybrid level route numeric string",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				routes := value["levelRoutes"].([]any)
				routes[0].(map[string]any)["minLevel"] = "1"
				return value
			},
		},
		{
			name: "hybrid level route enabled string",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				routes := value["levelRoutes"].([]any)
				routes[0].(map[string]any)["enabled"] = "true"
				return value
			},
		},
		{
			name: "hybrid quality inspection null",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["qualityInspection"] = nil
				return value
			},
		},
		{
			name: "hybrid quality inspection array",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["qualityInspection"] = []any{}
				return value
			},
		},
		{
			name: "hybrid quality inspection boolean",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["qualityInspection"] = false
				return value
			},
		},
		{
			name: "hybrid quality inspection string",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				value["qualityInspection"] = "config"
				return value
			},
		},
		{
			name: "hybrid quality optional string array",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				quality := value["qualityInspection"].(map[string]any)
				quality["scoringModel"] = []any{"quality-model"}
				return value
			},
		},
		{
			name: "hybrid quality numeric boolean",
			mode: "hybrid_smart",
			config: func() any {
				value := validManagementHybridCreateConfig()
				quality := value["qualityInspection"].(map[string]any)
				quality["maxRetries"] = false
				return value
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyCreateStore()
			store.target = port.PublicGroupTarget{ID: "sys_owner", Status: "active"}
			store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
				ID: "group_1", SystemAccountID: "sys_owner", Enabled: true,
			}
			input := CreateInput{
				SystemAccountID: "sys_owner",
				Name:            "测试策略",
				Mode:            tt.mode,
				GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
			}
			if tt.normal {
				input.NormalRoutingConfig = NewConfigInput(tt.config(), true)
			} else {
				input.HybridRoutingConfig = NewConfigInput(tt.config(), true)
			}
			service, tx, invalidator, _ := newManagementRouteStrategyCreateService(store)

			_, err := service.Create(context.Background(), input)
			if _, ok := ValidationMessage(err); !ok {
				t.Fatalf("Create() error = %T %v, want typed validation error", err, err)
			}
			if tx.calls != 0 || invalidator.calls != 0 {
				t.Fatalf("transaction calls = %d, invalidation calls = %d, want 0", tx.calls, invalidator.calls)
			}
		})
	}
}

func TestServiceCreateAcceptsJSONAndGoNumericConfigTypes(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "json number", value: json.Number("2")},
		{name: "int", value: int(2)},
		{name: "int8", value: int8(2)},
		{name: "int16", value: int16(2)},
		{name: "int32", value: int32(2)},
		{name: "int64", value: int64(2)},
		{name: "uint", value: uint(2)},
		{name: "uint8", value: uint8(2)},
		{name: "uint16", value: uint16(2)},
		{name: "uint32", value: uint32(2)},
		{name: "uint64", value: uint64(2)},
		{name: "float32", value: float32(2)},
		{name: "float64", value: float64(2)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyCreateStore()
			store.target = port.PublicGroupTarget{ID: "sys_owner", Status: "active"}
			store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
				ID: "group_1", SystemAccountID: "sys_owner", Enabled: true,
			}
			service, _, _, _ := newManagementRouteStrategyCreateService(store)

			result, err := service.Create(context.Background(), CreateInput{
				SystemAccountID: "sys_owner",
				Name:            "数字类型",
				GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
				NormalRoutingConfig: NewConfigInput(map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig": map[string]any{
						"maxFirstByteRetriesPerRequest": tt.value,
					},
				}, true),
			})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if result.NormalRoutingConfig == nil ||
				result.NormalRoutingConfig.SpeedFirstConfig == nil ||
				result.NormalRoutingConfig.SpeedFirstConfig.MaxFirstByteRetriesPerRequest != 2 {
				t.Fatalf("normal config = %+v", result.NormalRoutingConfig)
			}
		})
	}
}

func TestServiceCreateStoresSpeedFirstConfigAndOmitsOwnerForSelf(t *testing.T) {
	store := newManagementRouteStrategyCreateStore()
	store.target = port.PublicGroupTarget{ID: "sys_self", DisplayName: "本人", Status: "active"}
	store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
		ID: "group_1", SystemAccountID: "sys_self", Name: "分组一", Enabled: true,
	}
	service, _, _, _ := newManagementRouteStrategyCreateService(store)

	result, err := service.Create(context.Background(), CreateInput{
		SystemAccountID: "sys_self",
		Name:            "速度优先",
		Description:     stringPointer(" \t "),
		GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
		NormalRoutingConfig: NewConfigInput(map[string]any{
			"schedulingPreference": "speed_first",
			"speedFirstConfig": map[string]any{
				"slowTriggerCount": json.Number("4"),
			},
		}, true),
		HybridRoutingConfig: NewConfigInput(nil, true),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.SystemAccountID != "" || result.SystemAccountName != "" {
		t.Fatalf("self owner fields = (%q, %q)", result.SystemAccountID, result.SystemAccountName)
	}
	if result.Description != nil {
		t.Fatalf("description = %q, want nil", *result.Description)
	}
	if result.NormalRoutingConfig == nil || result.NormalRoutingConfig.SpeedFirstConfig == nil {
		t.Fatalf("normal config = %+v", result.NormalRoutingConfig)
	}
	speed := result.NormalRoutingConfig.SpeedFirstConfig
	if speed.SlowTriggerCount != 4 ||
		speed.FirstByteThresholdMs != defaultFirstByteThresholdMs ||
		speed.MaxFirstByteRetriesPerRequest != defaultMaxFirstByteRetriesPerRequest {
		t.Fatalf("speed config = %+v", speed)
	}
	if len(store.createInputs) != 1 || store.createInputs[0].ConfigJSON == nil {
		t.Fatalf("create input config = %+v", store.createInputs)
	}
	parsed, err := parseRouteStrategyRuntimeConfig(store.createInputs[0].ConfigJSON)
	if err != nil {
		t.Fatalf("parse stored config error = %v", err)
	}
	if parsed.NormalRoutingConfig == nil || parsed.NormalRoutingConfig.SpeedFirstConfig == nil {
		t.Fatalf("stored config = %+v", parsed)
	}
}

func TestServiceCreateAcceptsBindableAuthorizedGroupReturnedByStore(t *testing.T) {
	store := newManagementRouteStrategyCreateStore()
	store.target = port.PublicGroupTarget{ID: "sys_grantee", DisplayName: "被授权方", Status: "active"}
	store.groups["group_shared"] = port.PublicRouteStrategyBindableGroup{
		ID:              "group_shared",
		SystemAccountID: "sys_owner",
		Name:            "授权分组",
		ProviderCode:    "openai",
		Enabled:         true,
	}
	service, _, _, _ := newManagementRouteStrategyCreateService(store)

	result, err := service.Create(context.Background(), CreateInput{
		SystemAccountID: "sys_grantee",
		Name:            "授权策略",
		GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_shared"}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(result.GroupBindings) != 1 ||
		result.GroupBindings[0].GroupID != "group_shared" ||
		result.GroupBindings[0].GroupName != "授权分组" ||
		!result.GroupBindings[0].GroupEnabled {
		t.Fatalf("group bindings = %+v", result.GroupBindings)
	}
}

func TestServiceCreateReturnsTypedValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*managementRouteStrategyCreateStore, *CreateInput)
		wantText string
	}{
		{
			name: "target missing",
			mutate: func(store *managementRouteStrategyCreateStore, _ *CreateInput) {
				store.targetFound = false
			},
			wantText: "目标系统账户不存在",
		},
		{
			name: "blank name",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Name = " \t "
			},
			wantText: "策略路由名称不能为空",
		},
		{
			name: "invalid mode",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "random"
			},
			wantText: "路由策略模式无效",
		},
		{
			name: "mode is not trimmed",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = " normal "
			},
			wantText: "路由策略模式无效",
		},
		{
			name: "invalid status",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Status = "paused"
			},
			wantText: "策略路由状态无效",
		},
		{
			name: "status is not trimmed",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Status = " active "
			},
			wantText: "策略路由状态无效",
		},
		{
			name: "missing bindings",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings = nil
			},
			wantText: "策略路由至少需要绑定一个分组",
		},
		{
			name: "too many bindings",
			mutate: func(store *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "weighted"
				input.GroupBindings = make([]CreateGroupBindingInput, 21)
				for index := range input.GroupBindings {
					groupID := "group_" + string(rune('a'+index))
					input.GroupBindings[index] = CreateGroupBindingInput{GroupID: groupID}
					store.groups[groupID] = port.PublicRouteStrategyBindableGroup{
						ID: groupID, SystemAccountID: "sys_owner", Enabled: true,
					}
				}
			},
			wantText: "策略路由最多绑定 20 个分组",
		},
		{
			name: "duplicate group",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "weighted"
				input.GroupBindings = []CreateGroupBindingInput{
					{GroupID: "group_1"},
					{GroupID: " group_1 "},
				}
			},
			wantText: "策略路由绑定分组不能重复",
		},
		{
			name: "invalid priority",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings[0].Priority = -1
			},
			wantText: "策略路由分组优先级必须是大于 0 的整数",
		},
		{
			name: "invalid weight",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings[0].Weight = 101
			},
			wantText: "策略路由分组权重必须是 1-100",
		},
		{
			name: "invalid binding status",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings[0].Status = "paused"
			},
			wantText: "策略路由分组绑定状态无效",
		},
		{
			name: "binding status is not trimmed",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings[0].Status = " active "
			},
			wantText: "策略路由分组绑定状态无效",
		},
		{
			name: "all bindings disabled",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings[0].Status = "disabled"
			},
			wantText: "策略路由至少需要一个启用分组",
		},
		{
			name: "duplicate active priority",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "weighted"
				input.GroupBindings = []CreateGroupBindingInput{
					{GroupID: "group_1", Priority: 1},
					{GroupID: "group_2", Priority: 1},
				}
			},
			wantText: "策略路由启用分组优先级不能重复",
		},
		{
			name: "group not authorized",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings[0].GroupID = "group_missing"
			},
			wantText: "策略路由只能绑定自己的分组或有效授权给自己的分组",
		},
		{
			name: "active disabled group",
			mutate: func(store *managementRouteStrategyCreateStore, _ *CreateInput) {
				group := store.groups["group_1"]
				group.Enabled = false
				store.groups["group_1"] = group
			},
			wantText: "策略路由不能启用已停用分组",
		},
		{
			name: "normal has disabled extra binding",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.GroupBindings = []CreateGroupBindingInput{
					{GroupID: "group_1"},
					{GroupID: "group_2", Status: "disabled"},
				}
			},
			wantText: "普通路由只能绑定一个启用分组",
		},
		{
			name: "failover requires two bindings",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "failover"
			},
			wantText: "故障回退路由需要一个主用分组和至少一个备用分组",
		},
		{
			name: "failover first sorted binding must be active",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "failover"
				input.GroupBindings = []CreateGroupBindingInput{
					{GroupID: "group_2", Priority: 2},
					{GroupID: "group_1", Priority: 1, Status: "disabled"},
				}
			},
			wantText: "故障回退路由的主用分组必须启用",
		},
		{
			name: "failover requires active backup",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "failover"
				input.GroupBindings = []CreateGroupBindingInput{
					{GroupID: "group_1", Priority: 1},
					{GroupID: "group_2", Priority: 2, Status: "disabled"},
				}
			},
			wantText: "故障回退路由至少需要一个启用备用分组",
		},
		{
			name: "normal rejects hybrid sibling config",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.HybridRoutingConfig = NewConfigInput(map[string]any{}, true)
			},
			wantText: "普通路由不能配置混合评分规则",
		},
		{
			name: "hybrid requires config",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "hybrid_smart"
			},
			wantText: "混合路由配置不能为空",
		},
		{
			name: "hybrid rejects normal sibling config",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "hybrid_smart"
				input.NormalRoutingConfig = NewConfigInput(map[string]any{}, true)
				input.HybridRoutingConfig = NewConfigInput(validManagementHybridCreateConfig(), true)
			},
			wantText: "只有普通路由可以配置调度偏好",
		},
		{
			name: "non matching mode rejects normal config",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "weighted"
				input.NormalRoutingConfig = NewConfigInput(map[string]any{}, true)
			},
			wantText: "只有普通路由可以配置调度偏好",
		},
		{
			name: "hybrid rejects incomplete config",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.Mode = "hybrid_smart"
				input.HybridRoutingConfig = NewConfigInput(map[string]any{
					"scoringModel": "model",
					"levelRoutes": []any{
						map[string]any{"minLevel": 1, "maxLevel": 3, "targetModel": "a"},
						map[string]any{"minLevel": 5, "maxLevel": 10, "targetModel": "b"},
					},
				}, true)
			},
			wantText: "混合路由第 2 个等级范围必须从等级 4 开始",
		},
		{
			name: "normal rejects unknown nested config key",
			mutate: func(_ *managementRouteStrategyCreateStore, input *CreateInput) {
				input.NormalRoutingConfig = NewConfigInput(map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig": map[string]any{
						"slowTriggerCount": 4,
						"unknown":          true,
					},
				}, true)
			},
			wantText: "策略路由配置包含未知字段：unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyCreateStore()
			store.target = port.PublicGroupTarget{ID: "sys_owner", DisplayName: "所有者", Status: "active"}
			store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
				ID: "group_1", SystemAccountID: "sys_owner", Name: "分组一", Enabled: true,
			}
			store.groups["group_2"] = port.PublicRouteStrategyBindableGroup{
				ID: "group_2", SystemAccountID: "sys_owner", Name: "分组二", Enabled: true,
			}
			input := CreateInput{
				SystemAccountID: "sys_owner",
				Name:            "测试策略",
				GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
			}
			tt.mutate(store, &input)
			service, _, invalidator, _ := newManagementRouteStrategyCreateService(store)

			_, err := service.Create(context.Background(), input)
			message, ok := ValidationMessage(err)
			if !ok {
				t.Fatalf("Create() error = %T %v, want typed validation error", err, err)
			}
			if !strings.Contains(message, tt.wantText) {
				t.Fatalf("validation message = %q, want contains %q", message, tt.wantText)
			}
			if invalidator.calls != 0 {
				t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
			}
		})
	}
}

func TestServiceCreateMapsDuplicateNameToTypedConflict(t *testing.T) {
	store := newManagementRouteStrategyCreateStore()
	store.target = port.PublicGroupTarget{ID: "sys_owner", DisplayName: "所有者", Status: "active"}
	store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
		ID: "group_1", SystemAccountID: "sys_owner", Enabled: true,
	}
	store.createErr = port.ErrPublicRouteStrategyDuplicateName
	service, _, invalidator, _ := newManagementRouteStrategyCreateService(store)

	_, err := service.Create(context.Background(), CreateInput{
		SystemAccountID: "sys_owner",
		Name:            " 重复策略 ",
		GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
	})
	message, ok := NameExistsMessage(err)
	if !ok {
		t.Fatalf("Create() error = %T %v, want typed name conflict", err, err)
	}
	if message != "策略路由名称已存在：重复策略" {
		t.Fatalf("conflict message = %q", message)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceCreatePreservesUnknownStoreError(t *testing.T) {
	store := newManagementRouteStrategyCreateStore()
	store.target = port.PublicGroupTarget{ID: "sys_owner", Status: "active"}
	store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
		ID: "group_1", SystemAccountID: "sys_owner", Enabled: true,
	}
	storeErr := errors.New("database unavailable")
	store.createErr = storeErr
	service, _, _, _ := newManagementRouteStrategyCreateService(store)

	_, err := service.Create(context.Background(), CreateInput{
		SystemAccountID: "sys_owner",
		Name:            "测试策略",
		GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
	})
	if !errors.Is(err, storeErr) {
		t.Fatalf("Create() error = %v, want %v", err, storeErr)
	}
	if _, ok := ValidationMessage(err); ok {
		t.Fatalf("unknown error must not become validation error: %v", err)
	}
}

func TestServiceCreateDoesNotInvalidateWhenTransactionCommitFails(t *testing.T) {
	store := newManagementRouteStrategyCreateStore()
	store.target = port.PublicGroupTarget{ID: "sys_owner", Status: "active"}
	store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
		ID: "group_1", SystemAccountID: "sys_owner", Enabled: true,
	}
	service, tx, invalidator, _ := newManagementRouteStrategyCreateService(store)
	commitErr := errors.New("commit failed")
	tx.afterErr = commitErr

	_, err := service.Create(context.Background(), CreateInput{
		SystemAccountID: "sys_owner",
		Name:            "测试策略",
		GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("Create() error = %v, want %v", err, commitErr)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceCreateInvalidationUsesDetachedTimeoutAndFailureOnlyWarns(t *testing.T) {
	store := newManagementRouteStrategyCreateStore()
	store.target = port.PublicGroupTarget{ID: "sys_owner", Status: "active"}
	store.groups["group_1"] = port.PublicRouteStrategyBindableGroup{
		ID: "group_1", SystemAccountID: "sys_owner", Enabled: true,
	}
	var logs bytes.Buffer
	service, _, invalidator, _ := newManagementRouteStrategyCreateServiceWithLogger(
		store,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	invalidator.err = errors.New("invalidate failed")
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Create(parent, CreateInput{
		SystemAccountID: "sys_owner",
		Name:            "测试策略",
		GroupBindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.ID == "" {
		t.Fatalf("result = %+v", result)
	}
	if invalidator.calls != 1 || invalidator.contextErr != nil {
		t.Fatalf("invalidation calls = %d context error = %v", invalidator.calls, invalidator.contextErr)
	}
	if invalidator.deadlineRemaining <= 0 || invalidator.deadlineRemaining > 5*time.Second {
		t.Fatalf("deadline remaining = %s, want within 5s", invalidator.deadlineRemaining)
	}
	if !strings.Contains(logs.String(), "策略路由创建后网关运行态失效失败") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func validManagementHybridCreateConfig() map[string]any {
	return map[string]any{
		"scoringGroupId":    " score_group ",
		"scoringModel":      " scorer-model ",
		"qualityPreference": "quality_first",
		"levelRoutes": []any{
			map[string]any{
				"minLevel":    1,
				"maxLevel":    3,
				"targetModel": " model-a ",
			},
			map[string]any{
				"minLevel":    4,
				"maxLevel":    10,
				"targetModel": " model-b ",
			},
		},
		"qualityInspection": map[string]any{
			"enabled":      true,
			"scoringModel": " quality-model ",
		},
	}
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}

func newManagementRouteStrategyCreateService(
	store *managementRouteStrategyCreateStore,
) (*Service, *managementRouteStrategyCreateTransactor, *managementRouteStrategyInvalidator, *bytes.Buffer) {
	return newManagementRouteStrategyCreateServiceWithLogger(store, nil)
}

func newManagementRouteStrategyCreateServiceWithLogger(
	store *managementRouteStrategyCreateStore,
	logger *slog.Logger,
) (*Service, *managementRouteStrategyCreateTransactor, *managementRouteStrategyInvalidator, *bytes.Buffer) {
	tx := &managementRouteStrategyCreateTransactor{store: store}
	invalidator := &managementRouteStrategyInvalidator{}
	var logs bytes.Buffer
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(&logs, nil))
	}
	sequence := 0
	service := NewServiceWithOptions(ServiceOptions{
		CreateStore: store,
		Transactor:  tx,
		Invalidator: invalidator,
		Logger:      logger,
		Now:         func() time.Time { return managementRouteStrategyCreateTestNow },
		NewID: func(prefix string) string {
			sequence++
			return prefix + "_test_" + string(rune('0'+sequence))
		},
	})
	return service, tx, invalidator, &logs
}

type managementRouteStrategyCreateTransactor struct {
	store     port.PublicRouteStrategyStore
	calls     int
	beforeErr error
	afterErr  error
}

func (t *managementRouteStrategyCreateTransactor) PublicRouteStrategyInTx(
	ctx context.Context,
	fn func(context.Context, port.PublicRouteStrategyStore) error,
) error {
	t.calls++
	if t.beforeErr != nil {
		return t.beforeErr
	}
	if err := fn(ctx, t.store); err != nil {
		return err
	}
	return t.afterErr
}

type managementRouteStrategyInvalidator struct {
	calls             int
	reasons           []string
	err               error
	contextErr        error
	deadlineRemaining time.Duration
}

func (i *managementRouteStrategyInvalidator) InvalidateGatewayRuntime(
	ctx context.Context,
	reason string,
) error {
	i.calls++
	i.reasons = append(i.reasons, reason)
	i.contextErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		i.deadlineRemaining = time.Until(deadline)
	}
	return i.err
}

type managementRouteStrategyCreateStore struct {
	target       port.PublicGroupTarget
	targetFound  bool
	targetErr    error
	groups       map[string]port.PublicRouteStrategyBindableGroup
	groupsErr    error
	createInputs []port.PublicRouteStrategyCreateInput
	createErr    error
}

func newManagementRouteStrategyCreateStore() *managementRouteStrategyCreateStore {
	return &managementRouteStrategyCreateStore{
		targetFound: true,
		groups:      map[string]port.PublicRouteStrategyBindableGroup{},
	}
}

func (s *managementRouteStrategyCreateStore) FindPublicRouteStrategyTargetByUsername(
	context.Context,
	string,
) (port.PublicGroupTarget, bool, error) {
	return port.PublicGroupTarget{}, false, errors.New("unexpected username target lookup")
}

func (s *managementRouteStrategyCreateStore) FindPublicRouteStrategyTargetByID(
	_ context.Context,
	id string,
) (port.PublicGroupTarget, bool, error) {
	if s.targetErr != nil {
		return port.PublicGroupTarget{}, false, s.targetErr
	}
	if !s.targetFound || s.target.ID != id {
		return port.PublicGroupTarget{}, false, nil
	}
	return s.target, true, nil
}

func (s *managementRouteStrategyCreateStore) ListPublicRouteStrategies(
	context.Context,
	port.PublicRouteStrategyListInput,
) (port.PublicRouteStrategyListPage, error) {
	return port.PublicRouteStrategyListPage{}, errors.New("unexpected route strategy list")
}

func (s *managementRouteStrategyCreateStore) FindPublicRouteStrategyByID(
	context.Context,
	string,
) (port.PublicRouteStrategySummary, bool, error) {
	return port.PublicRouteStrategySummary{}, false, errors.New("unexpected route strategy lookup")
}

func (s *managementRouteStrategyCreateStore) FindPublicRouteStrategyBindableGroups(
	_ context.Context,
	_ string,
	groupIDs []string,
) ([]port.PublicRouteStrategyBindableGroup, error) {
	if s.groupsErr != nil {
		return nil, s.groupsErr
	}
	groups := make([]port.PublicRouteStrategyBindableGroup, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if group, ok := s.groups[groupID]; ok {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

func (s *managementRouteStrategyCreateStore) CreatePublicRouteStrategy(
	_ context.Context,
	input port.PublicRouteStrategyCreateInput,
) (port.PublicRouteStrategySummary, error) {
	s.createInputs = append(s.createInputs, input)
	if s.createErr != nil {
		return port.PublicRouteStrategySummary{}, s.createErr
	}
	bindings := make([]port.PublicRouteStrategyGroupBindingSummary, 0, len(input.Bindings))
	for _, binding := range input.Bindings {
		group := s.groups[binding.GroupID]
		bindings = append(bindings, port.PublicRouteStrategyGroupBindingSummary{
			ID:           binding.ID,
			GroupID:      binding.GroupID,
			GroupName:    group.Name,
			ProviderCode: group.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       binding.Status,
			GroupEnabled: group.Enabled,
		})
	}
	return port.PublicRouteStrategySummary{
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
		Name:            input.Name,
		Description:     input.Description,
		Mode:            input.Mode,
		Status:          input.Status,
		IsDefault:       true,
		ConfigJSON:      input.ConfigJSON,
		GroupBindings:   bindings,
		APIKeyCount:     9,
		CreatedAt:       input.Now,
		UpdatedAt:       input.Now,
	}, nil
}

func (s *managementRouteStrategyCreateStore) UpdatePublicRouteStrategy(
	context.Context,
	port.PublicRouteStrategyUpdateInput,
) (port.PublicRouteStrategySummary, bool, error) {
	return port.PublicRouteStrategySummary{}, false, errors.New("unexpected route strategy update")
}

func (s *managementRouteStrategyCreateStore) DeletePublicRouteStrategy(
	context.Context,
	string,
	string,
) (bool, error) {
	return false, errors.New("unexpected route strategy delete")
}

func (s *managementRouteStrategyCreateStore) PublicRouteStrategyAPIKeyCount(
	context.Context,
	string,
	string,
) (int64, error) {
	return 0, errors.New("unexpected api key count")
}
