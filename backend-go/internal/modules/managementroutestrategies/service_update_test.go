package managementroutestrategies

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

var managementRouteStrategyUpdateTestNow = time.Date(2026, 7, 12, 9, 30, 0, 0, time.UTC)

func TestServicePrepareUpdatePreloadsVisibleStateWithoutPrematureNotFound(t *testing.T) {
	t.Run("visible malformed current config returns validation error", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.current.ConfigJSON = stringPointer(`{broken`)
		service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

		err := service.PrepareUpdate(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
		})

		message, ok := ValidationMessage(err)
		if !ok || !strings.Contains(message, "现有策略路由配置无效") {
			t.Fatalf("PrepareUpdate() error=%T %v message=%q", err, err, message)
		}
		if store.currentFindCalls != 1 ||
			store.targetFindCalls != 1 ||
			tx.calls != 0 ||
			invalidator.calls != 0 {
			t.Fatalf(
				"current=%d target=%d tx=%d invalidation=%d",
				store.currentFindCalls,
				store.targetFindCalls,
				tx.calls,
				invalidator.calls,
			)
		}
	})

	t.Run("database preload error is returned", func(t *testing.T) {
		wantErr := errors.New("preload database unavailable")
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.currentErr = wantErr
		service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

		err := service.PrepareUpdate(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
		})

		if !errors.Is(err, wantErr) ||
			store.currentFindCalls != 1 ||
			store.targetFindCalls != 0 ||
			tx.calls != 0 ||
			invalidator.calls != 0 {
			t.Fatalf(
				"error=%v current=%d target=%d tx=%d invalidation=%d",
				err,
				store.currentFindCalls,
				store.targetFindCalls,
				tx.calls,
				invalidator.calls,
			)
		}
	})

	tests := []struct {
		name            string
		mutate          func(*managementRouteStrategyUpdateStore)
		input           UpdateInput
		wantTargetCalls int
	}{
		{
			name: "missing does not return not found",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.currentFound = false
			},
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				RouteStrategyID:      "route_missing",
			},
		},
		{
			name: "owner mismatch stays invisible",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.current.ConfigJSON = stringPointer(`{broken`)
			},
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      "sys_other",
				RouteStrategyID:      "route_1",
			},
		},
		{
			name: "target missing does not return not found",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.targetFound = false
			},
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
			},
			wantTargetCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			tt.mutate(store)
			service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

			err := service.PrepareUpdate(context.Background(), tt.input)

			if err != nil {
				t.Fatalf("PrepareUpdate() error=%T %v, want nil", err, err)
			}
			if store.currentFindCalls != 1 ||
				store.targetFindCalls != tt.wantTargetCalls ||
				tx.calls != 0 ||
				invalidator.calls != 0 {
				t.Fatalf(
					"current=%d target=%d wantTarget=%d tx=%d invalidation=%d",
					store.currentFindCalls,
					store.targetFindCalls,
					tt.wantTargetCalls,
					tx.calls,
					invalidator.calls,
				)
			}
		})
	}
}

func TestServiceUpdateScopesAndReturnsCompleteBeforeAfter(t *testing.T) {
	tests := []struct {
		name          string
		actorID       string
		role          string
		ownerID       string
		selfOnly      bool
		currentOwner  string
		wantOwnerData bool
	}{
		{
			name:          "admin global by id",
			actorID:       "sys_admin",
			role:          "admin",
			currentOwner:  "sys_owner",
			wantOwnerData: true,
		},
		{
			name:          "super admin narrows owner",
			actorID:       "sys_admin",
			role:          "super_admin",
			ownerID:       "sys_owner",
			currentOwner:  "sys_owner",
			wantOwnerData: true,
		},
		{
			name:         "self forces actor owner and hides owner fields",
			actorID:      "sys_self",
			role:         "member",
			ownerID:      "sys_other",
			selfOnly:     true,
			currentOwner: "sys_self",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore(tt.currentOwner)
			store.target.Status = "disabled"
			wantCreatedAt := store.current.CreatedAt.UTC().Format(time.RFC3339Nano)
			wantUpdatedAt := store.current.UpdatedAt.UTC().Format(time.RFC3339Nano)
			service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)
			publisher := &routeStrategyPageDataPublisherStub{}
			service.pageDataPublisher = publisher

			result, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: tt.actorID,
				ActorRole:            tt.role,
				SystemAccountID:      tt.ownerID,
				SelfOnly:             tt.selfOnly,
				RouteStrategyID:      " route_1 ",
				HasName:              true,
				Name:                 " 更新后 ",
			})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if tx.calls != 1 || len(store.updateInputs) != 1 {
				t.Fatalf("transaction calls = %d update inputs = %d", tx.calls, len(store.updateInputs))
			}
			if result.OwnerSystemAccountID != tt.currentOwner {
				t.Fatalf("owner system account id = %q", result.OwnerSystemAccountID)
			}
			if result.Before.ID != "route_1" ||
				result.Before.Name != "原策略" ||
				result.Before.Description == nil ||
				*result.Before.Description != "原说明" ||
				result.Before.Mode != "normal" ||
				result.Before.Status != "active" ||
				!result.Before.IsDefault ||
				result.Before.APIKeyCount != 7 ||
				len(result.Before.GroupBindings) != 1 ||
				result.Before.GroupBindings[0].ID != "binding_old" ||
				result.Before.NormalRoutingConfig == nil ||
				result.Before.NormalRoutingConfig.SchedulingPreference != "speed_first" ||
				result.Before.CreatedAt != wantCreatedAt ||
				result.Before.UpdatedAt != wantUpdatedAt {
				t.Fatalf("before = %+v", result.Before)
			}
			if result.RouteStrategy.Name != "更新后" ||
				!result.RouteStrategy.IsDefault ||
				result.RouteStrategy.APIKeyCount != 7 ||
				len(result.RouteStrategy.GroupBindings) != 1 ||
				result.RouteStrategy.GroupBindings[0].ID == "binding_old" ||
				result.RouteStrategy.NormalRoutingConfig == nil ||
				result.RouteStrategy.NormalRoutingConfig.SchedulingPreference != "speed_first" ||
				result.RouteStrategy.CreatedAt != result.Before.CreatedAt ||
				result.RouteStrategy.UpdatedAt != managementRouteStrategyUpdateTestNow.Format(time.RFC3339Nano) {
				t.Fatalf("after = %+v", result.RouteStrategy)
			}
			if tt.wantOwnerData {
				if result.Before.SystemAccountID != tt.currentOwner ||
					result.Before.SystemAccountName != "停用所有者" ||
					result.RouteStrategy.SystemAccountID != tt.currentOwner ||
					result.RouteStrategy.SystemAccountName != "停用所有者" {
					t.Fatalf("owner fields before=%+v after=%+v", result.Before, result.RouteStrategy)
				}
			} else if result.Before.SystemAccountID != "" ||
				result.Before.SystemAccountName != "" ||
				result.RouteStrategy.SystemAccountID != "" ||
				result.RouteStrategy.SystemAccountName != "" {
				t.Fatalf("self owner fields leaked before=%+v after=%+v", result.Before, result.RouteStrategy)
			}
			if store.bindableCalls != 1 {
				t.Fatalf("bindable lock calls = %d, want 1 without re-authorization", store.bindableCalls)
			}
			if invalidator.calls != 1 ||
				len(invalidator.reasons) != 1 ||
				invalidator.reasons[0] != RouteStrategyUpdatedReason {
				t.Fatalf("invalidation calls=%d reasons=%#v", invalidator.calls, invalidator.reasons)
			}
			assertRouteStrategyPageDataReset(t, publisher)
		})
	}
}

func TestServiceUpdateReturnsTypedNotFoundForMissingOrUnauthorized(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*managementRouteStrategyUpdateStore)
		input  UpdateInput
		wantID string
	}{
		{
			name: "missing route strategy",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.currentFound = false
			},
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				RouteStrategyID:      "route_missing",
				HasName:              true,
				Name:                 "名称",
			},
			wantID: "route_missing",
		},
		{
			name: "admin owner narrowing mismatch",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      "sys_other",
				RouteStrategyID:      "route_1",
				HasName:              true,
				Name:                 "名称",
			},
			wantID: "route_1",
		},
		{
			name: "self cannot update another owner",
			input: UpdateInput{
				ActorSystemAccountID: "sys_self",
				ActorRole:            "member",
				RouteStrategyID:      "route_1",
				HasName:              true,
				Name:                 "名称",
			},
			wantID: "route_1",
		},
		{
			name: "owner target disappeared",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.targetFound = false
			},
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				RouteStrategyID:      "route_1",
				HasName:              true,
				Name:                 "名称",
			},
			wantID: "route_1",
		},
		{
			name: "update lost after lookup",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.updateFound = false
			},
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				RouteStrategyID:      "route_1",
				HasName:              true,
				Name:                 "名称",
			},
			wantID: "route_1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			if tt.mutate != nil {
				tt.mutate(store)
			}
			service, _, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

			_, err := service.Update(context.Background(), tt.input)
			var notFound *NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("Update() error = %T %v, want typed not found", err, err)
			}
			if notFound.RouteStrategyID != tt.wantID {
				t.Fatalf("not found route id = %q, want %q", notFound.RouteStrategyID, tt.wantID)
			}
			message, ok := NotFoundMessage(err)
			if !ok || message != "策略路由不存在" {
				t.Fatalf("not found message = %q, %v", message, ok)
			}
			if invalidator.calls != 0 {
				t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
			}
		})
	}
}

func TestServiceUpdateValidatesPatchBeforeNotFoundForInvisibleCurrent(t *testing.T) {
	resources := []struct {
		name    string
		routeID string
		ownerID string
		mutate  func(*managementRouteStrategyUpdateStore)
	}{
		{
			name:    "missing",
			routeID: "route_missing",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.currentFound = false
			},
		},
		{
			name:    "owner mismatch",
			routeID: "route_1",
			ownerID: "sys_other",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				store.current.ConfigJSON = stringPointer(`{broken`)
			},
		},
	}
	patches := []struct {
		name     string
		mutate   func(*UpdateInput)
		wantText string
	}{
		{
			name: "empty patch",
			mutate: func(_ *UpdateInput) {
			},
			wantText: "请提供要修改的策略路由内容",
		},
		{
			name: "invalid name",
			mutate: func(input *UpdateInput) {
				input.HasName = true
				input.Name = " \t "
			},
			wantText: "策略路由名称不能为空",
		},
		{
			name: "invalid mode",
			mutate: func(input *UpdateInput) {
				input.HasMode = true
				input.Mode = "random"
			},
			wantText: "路由策略模式无效",
		},
		{
			name: "invalid status",
			mutate: func(input *UpdateInput) {
				input.HasStatus = true
				input.Status = "paused"
			},
			wantText: "策略路由状态无效",
		},
		{
			name: "invalid bindings",
			mutate: func(input *UpdateInput) {
				input.HasGroupBindings = true
			},
			wantText: "策略路由至少需要绑定一个分组",
		},
		{
			name: "invalid normal config",
			mutate: func(input *UpdateInput) {
				input.NormalRoutingConfig = NewConfigInput(map[string]any{
					"schedulingPreference": "invalid",
				}, true)
			},
			wantText: "普通路由调度偏好无效",
		},
		{
			name: "cost first still validates explicit speed config",
			mutate: func(input *UpdateInput) {
				input.NormalRoutingConfig = NewConfigInput(map[string]any{
					"schedulingPreference": "cost_first",
					"speedFirstConfig": map[string]any{
						"slowTriggerCount": 999,
					},
				}, true)
			},
			wantText: "速度优先触发次数必须是 2-10",
		},
		{
			name: "invalid hybrid config",
			mutate: func(input *UpdateInput) {
				config := validManagementHybridCreateConfig()
				config["qualityPreference"] = "invalid"
				input.HybridRoutingConfig = NewConfigInput(config, true)
			},
			wantText: "混合路由质量偏好无效",
		},
	}

	for _, resource := range resources {
		for _, patch := range patches {
			t.Run(resource.name+"_"+patch.name, func(t *testing.T) {
				store := newManagementRouteStrategyUpdateStore("sys_owner")
				resource.mutate(store)
				service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)
				input := UpdateInput{
					ActorSystemAccountID: "sys_admin",
					ActorRole:            "admin",
					SystemAccountID:      resource.ownerID,
					RouteStrategyID:      resource.routeID,
				}
				patch.mutate(&input)

				_, err := service.Update(context.Background(), input)
				message, ok := ValidationMessage(err)
				if !ok || !strings.Contains(message, patch.wantText) {
					t.Fatalf("Update() error = %T %v, message=%q", err, err, message)
				}
				if tx.calls != 0 ||
					store.currentFindCalls != 1 ||
					store.targetFindCalls != 0 ||
					len(store.updateInputs) != 0 ||
					invalidator.calls != 0 {
					t.Fatalf(
						"transaction calls=%d current finds=%d target finds=%d updates=%d invalidation calls=%d",
						tx.calls,
						store.currentFindCalls,
						store.targetFindCalls,
						len(store.updateInputs),
						invalidator.calls,
					)
				}
			})
		}
	}
}

func TestServiceUpdateReturnsNotFoundAfterValidNestedConfigSchema(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*UpdateInput)
	}{
		{
			name: "normal config",
			apply: func(input *UpdateInput) {
				input.NormalRoutingConfig = NewConfigInput(map[string]any{
					"schedulingPreference": "speed_first",
					"speedFirstConfig": map[string]any{
						"slowTriggerCount": 5,
					},
				}, true)
			},
		},
		{
			name: "hybrid config",
			apply: func(input *UpdateInput) {
				input.HybridRoutingConfig = NewConfigInput(
					validManagementHybridCreateConfig(),
					true,
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			store.currentFound = false
			service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

			input := UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				RouteStrategyID:      "route_missing",
			}
			tt.apply(&input)
			_, err := service.Update(context.Background(), input)

			var notFound *NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("Update() error = %T %v, want typed not found", err, err)
			}
			if tx.calls != 0 || len(store.updateInputs) != 0 || invalidator.calls != 0 {
				t.Fatalf(
					"transactions=%d updates=%d invalidations=%d",
					tx.calls,
					len(store.updateInputs),
					invalidator.calls,
				)
			}
		})
	}
}

func TestServiceUpdateValidatesPatchPresenceAndScalarFields(t *testing.T) {
	tests := []struct {
		name     string
		input    UpdateInput
		wantText string
	}{
		{
			name: "empty patch",
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
			},
			wantText: "请提供要修改的策略路由内容",
		},
		{
			name: "blank name",
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasName:              true,
				Name:                 " \t ",
			},
			wantText: "策略路由名称不能为空",
		},
		{
			name: "invalid mode",
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasMode:              true,
				Mode:                 " normal ",
			},
			wantText: "路由策略模式无效",
		},
		{
			name: "invalid status",
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasStatus:            true,
				Status:               "paused",
			},
			wantText: "策略路由状态无效",
		},
		{
			name: "invalid bindings",
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasGroupBindings:     true,
			},
			wantText: "策略路由至少需要绑定一个分组",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

			_, err := service.Update(context.Background(), tt.input)
			message, ok := ValidationMessage(err)
			if !ok || !strings.Contains(message, tt.wantText) {
				t.Fatalf("Update() error = %T %v, message = %q", err, err, message)
			}
			if tx.calls != 0 ||
				store.currentFindCalls != 1 ||
				store.targetFindCalls != 1 ||
				len(store.updateInputs) != 0 ||
				invalidator.calls != 0 {
				t.Fatalf(
					"transaction calls=%d current finds=%d target finds=%d updates=%d invalidation calls=%d",
					tx.calls,
					store.currentFindCalls,
					store.targetFindCalls,
					len(store.updateInputs),
					invalidator.calls,
				)
			}
		})
	}
}

func TestServiceUpdateParsesCurrentConfigBeforePatchValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*UpdateInput)
	}{
		{
			name: "empty patch",
			mutate: func(_ *UpdateInput) {
			},
		},
		{
			name: "invalid name",
			mutate: func(input *UpdateInput) {
				input.HasName = true
				input.Name = " \t "
			},
		},
		{
			name: "invalid mode",
			mutate: func(input *UpdateInput) {
				input.HasMode = true
				input.Mode = "random"
			},
		},
		{
			name: "invalid status",
			mutate: func(input *UpdateInput) {
				input.HasStatus = true
				input.Status = "paused"
			},
		},
		{
			name: "invalid bindings",
			mutate: func(input *UpdateInput) {
				input.HasGroupBindings = true
			},
		},
		{
			name: "invalid normal config",
			mutate: func(input *UpdateInput) {
				input.NormalRoutingConfig = NewConfigInput(map[string]any{
					"schedulingPreference": "invalid",
				}, true)
			},
		},
		{
			name: "invalid hybrid config",
			mutate: func(input *UpdateInput) {
				config := validManagementHybridCreateConfig()
				config["qualityPreference"] = "invalid"
				input.HybridRoutingConfig = NewConfigInput(config, true)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			store.current.ConfigJSON = stringPointer(`{broken`)
			service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)
			input := UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
			}
			tt.mutate(&input)

			_, err := service.Update(context.Background(), input)
			message, ok := ValidationMessage(err)
			if !ok || !strings.Contains(message, "现有策略路由配置无效") {
				t.Fatalf("Update() error = %T %v, message=%q", err, err, message)
			}
			if tx.calls != 0 ||
				store.currentFindCalls != 1 ||
				store.targetFindCalls != 1 ||
				len(store.updateInputs) != 0 ||
				invalidator.calls != 0 {
				t.Fatalf(
					"transaction calls=%d current finds=%d target finds=%d updates=%d invalidation calls=%d",
					tx.calls,
					store.currentFindCalls,
					store.targetFindCalls,
					len(store.updateInputs),
					invalidator.calls,
				)
			}
		})
	}

	t.Run("target missing still parses visible current config", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.targetFound = false
		store.current.ConfigJSON = stringPointer(`{broken`)
		service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

		_, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasMode:              true,
			Mode:                 "random",
		})
		message, ok := ValidationMessage(err)
		if !ok || !strings.Contains(message, "现有策略路由配置无效") {
			t.Fatalf("Update() error = %T %v, message=%q", err, err, message)
		}
		if tx.calls != 0 ||
			store.currentFindCalls != 1 ||
			store.targetFindCalls != 1 ||
			len(store.updateInputs) != 0 ||
			invalidator.calls != 0 {
			t.Fatalf(
				"transaction calls=%d current finds=%d target finds=%d updates=%d invalidation calls=%d",
				tx.calls,
				store.currentFindCalls,
				store.targetFindCalls,
				len(store.updateInputs),
				invalidator.calls,
			)
		}
	})
}

func TestServiceUpdateSupportsNullableDescription(t *testing.T) {
	tests := []struct {
		name        string
		description *string
	}{
		{name: "null clears", description: nil},
		{name: "blank clears", description: stringPointer(" \t ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

			result, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasDescription:       true,
				Description:          tt.description,
			})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if result.Before.Description == nil || result.RouteStrategy.Description != nil {
				t.Fatalf("descriptions before=%v after=%v", result.Before.Description, result.RouteStrategy.Description)
			}
		})
	}
}

func TestServiceUpdateSupportsFiveModeTransitions(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		bindings     []CreateGroupBindingInput
		normalConfig ConfigInput
		hybridConfig ConfigInput
		wantConfig   bool
		wantNormal   bool
		wantHybrid   bool
	}{
		{
			name:       "normal",
			mode:       "normal",
			bindings:   []CreateGroupBindingInput{{GroupID: "group_1"}},
			wantNormal: true,
		},
		{
			name:         "hybrid smart",
			mode:         "hybrid_smart",
			bindings:     []CreateGroupBindingInput{{GroupID: "group_1"}},
			hybridConfig: NewConfigInput(validManagementHybridCreateConfig(), true),
			wantConfig:   true,
			wantHybrid:   true,
		},
		{
			name:     "weighted",
			mode:     "weighted",
			bindings: []CreateGroupBindingInput{{GroupID: "group_1", Weight: 37}},
		},
		{
			name: "failover",
			mode: "failover",
			bindings: []CreateGroupBindingInput{
				{GroupID: "group_2", Priority: 2},
				{GroupID: "group_1", Priority: 1},
			},
		},
		{
			name:     "round robin",
			mode:     "round_robin",
			bindings: []CreateGroupBindingInput{{GroupID: "group_2"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			store.current.Mode = port.PublicRouteStrategyModeWeighted
			store.current.ConfigJSON = stringPointer(`{"stale":true}`)
			service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

			result, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasMode:              true,
				Mode:                 tt.mode,
				HasGroupBindings:     true,
				GroupBindings:        tt.bindings,
				NormalRoutingConfig:  tt.normalConfig,
				HybridRoutingConfig:  tt.hybridConfig,
			})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			input := store.updateInputs[0]
			if string(input.Mode) != tt.mode || (input.ConfigJSON != nil) != tt.wantConfig {
				t.Fatalf("update input = %+v", input)
			}
			if (result.RouteStrategy.NormalRoutingConfig != nil) != tt.wantNormal ||
				(result.RouteStrategy.HybridRoutingConfig != nil) != tt.wantHybrid {
				t.Fatalf("after config = normal %+v hybrid %+v", result.RouteStrategy.NormalRoutingConfig, result.RouteStrategy.HybridRoutingConfig)
			}
			if tt.wantNormal &&
				result.RouteStrategy.NormalRoutingConfig.SchedulingPreference != defaultSchedulingPreference {
				t.Fatalf("normal config = %+v", result.RouteStrategy.NormalRoutingConfig)
			}
		})
	}
}

func TestServiceUpdateValidatesFinalModeWithOmittedBindings(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*managementRouteStrategyUpdateStore)
		mode     string
		wantText string
	}{
		{
			name:     "normal rejects existing extra binding",
			mutate:   setUpdateStoreFailoverBindings,
			mode:     "normal",
			wantText: "普通路由只能绑定一个启用分组",
		},
		{
			name:     "failover rejects existing single binding",
			mode:     "failover",
			wantText: "故障回退路由需要一个主用分组和至少一个备用分组",
		},
		{
			name: "failover requires active primary",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				setUpdateStoreFailoverBindings(store)
				store.current.GroupBindings[0].Status = port.PublicRouteStrategyStatusDisabled
			},
			mode:     "failover",
			wantText: "故障回退路由的主用分组必须启用",
		},
		{
			name: "failover requires active backup",
			mutate: func(store *managementRouteStrategyUpdateStore) {
				setUpdateStoreFailoverBindings(store)
				store.current.GroupBindings[1].Status = port.PublicRouteStrategyStatusDisabled
			},
			mode:     "failover",
			wantText: "故障回退路由至少需要一个启用备用分组",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			if tt.mutate != nil {
				tt.mutate(store)
			}
			service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

			_, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasMode:              true,
				Mode:                 tt.mode,
			})
			message, ok := ValidationMessage(err)
			if !ok || !strings.Contains(message, tt.wantText) {
				t.Fatalf("Update() error = %T %v, message = %q", err, err, message)
			}
			if store.bindableCalls != 1 || len(store.updateInputs) != 0 {
				t.Fatalf("bindable calls=%d update inputs=%d", store.bindableCalls, len(store.updateInputs))
			}
		})
	}
}

func TestServiceUpdateAppliesPartialConfigSemantics(t *testing.T) {
	t.Run("same normal omitted preserves current", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasStatus:            true,
			Status:               "disabled",
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.updateInputs[0].ConfigJSON == nil ||
			*store.updateInputs[0].ConfigJSON != *store.originalConfigJSON ||
			result.RouteStrategy.NormalRoutingConfig == nil ||
			result.RouteStrategy.NormalRoutingConfig.SchedulingPreference != "speed_first" {
			t.Fatalf("update input=%+v after=%+v", store.updateInputs[0], result.RouteStrategy)
		}
	})

	t.Run("explicit normal null resets to cost first", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			NormalRoutingConfig:  NewConfigInput(nil, true),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.updateInputs[0].ConfigJSON != nil ||
			result.Before.NormalRoutingConfig == nil ||
			result.Before.NormalRoutingConfig.SchedulingPreference != "speed_first" ||
			result.RouteStrategy.NormalRoutingConfig == nil ||
			result.RouteStrategy.NormalRoutingConfig.SchedulingPreference != defaultSchedulingPreference {
			t.Fatalf("update input=%+v before=%+v after=%+v", store.updateInputs[0], result.Before, result.RouteStrategy)
		}
	})

	t.Run("explicit normal replacement preserves valid before config", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			NormalRoutingConfig: NewConfigInput(map[string]any{
				"schedulingPreference": "speed_first",
				"speedFirstConfig": map[string]any{
					"slowTriggerCount": 5,
				},
			}, true),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if result.Before.NormalRoutingConfig == nil ||
			result.Before.NormalRoutingConfig.SchedulingPreference != "speed_first" ||
			result.Before.NormalRoutingConfig.SpeedFirstConfig == nil ||
			result.Before.NormalRoutingConfig.SpeedFirstConfig.SlowTriggerCount != 4 ||
			result.RouteStrategy.NormalRoutingConfig == nil ||
			result.RouteStrategy.NormalRoutingConfig.SpeedFirstConfig == nil ||
			result.RouteStrategy.NormalRoutingConfig.SpeedFirstConfig.SlowTriggerCount != 5 {
			t.Fatalf("before=%+v after=%+v", result.Before, result.RouteStrategy)
		}
	})

	t.Run("switch to normal omitted defaults to cost first", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.current.Mode = port.PublicRouteStrategyModeWeighted
		store.current.ConfigJSON = stringPointer(`{"stale":true}`)
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasMode:              true,
			Mode:                 "normal",
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.updateInputs[0].ConfigJSON != nil ||
			result.RouteStrategy.NormalRoutingConfig == nil ||
			result.RouteStrategy.NormalRoutingConfig.SchedulingPreference != defaultSchedulingPreference {
			t.Fatalf("update input=%+v after=%+v", store.updateInputs[0], result.RouteStrategy)
		}
	})

	t.Run("same hybrid omitted preserves valid config", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.current.Mode = port.PublicRouteStrategyModeHybridSmart
		store.current.ConfigJSON = hybridUpdateConfigJSON(t)
		store.originalConfigJSON = store.current.ConfigJSON
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasName:              true,
			Name:                 "新名称",
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.updateInputs[0].ConfigJSON == nil ||
			*store.updateInputs[0].ConfigJSON != *store.originalConfigJSON ||
			result.RouteStrategy.HybridRoutingConfig == nil {
			t.Fatalf("update input=%+v after=%+v", store.updateInputs[0], result.RouteStrategy)
		}
	})

	t.Run("explicit hybrid replacement preserves valid before config", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.current.Mode = port.PublicRouteStrategyModeHybridSmart
		store.current.ConfigJSON = hybridUpdateConfigJSON(t)
		replacement := validManagementHybridCreateConfig()
		replacement["qualityPreference"] = "cost_first"
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HybridRoutingConfig:  NewConfigInput(replacement, true),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if result.Before.HybridRoutingConfig == nil ||
			result.Before.HybridRoutingConfig["qualityPreference"] != "quality_first" ||
			result.RouteStrategy.HybridRoutingConfig == nil ||
			result.RouteStrategy.HybridRoutingConfig["qualityPreference"] != "cost_first" {
			t.Fatalf("before=%+v after=%+v", result.Before, result.RouteStrategy)
		}
	})

	t.Run("switch to hybrid omitted fails", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		_, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasMode:              true,
			Mode:                 "hybrid_smart",
		})
		message, ok := ValidationMessage(err)
		if !ok || !strings.Contains(message, "混合路由配置不能为空") {
			t.Fatalf("Update() error = %T %v", err, err)
		}
	})

	t.Run("switch to non config modes clears config", func(t *testing.T) {
		sourceModes := []port.PublicRouteStrategyMode{
			port.PublicRouteStrategyModeNormal,
			port.PublicRouteStrategyModeHybridSmart,
		}
		targetModes := []string{"weighted", "failover", "round_robin"}
		for _, sourceMode := range sourceModes {
			for _, targetMode := range targetModes {
				t.Run(string(sourceMode)+"_to_"+targetMode, func(t *testing.T) {
					store := newManagementRouteStrategyUpdateStore("sys_owner")
					store.current.Mode = sourceMode
					if sourceMode == port.PublicRouteStrategyModeHybridSmart {
						store.current.ConfigJSON = hybridUpdateConfigJSON(t)
					}
					if targetMode == "failover" {
						setUpdateStoreFailoverBindings(store)
					}
					service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

					result, err := service.Update(context.Background(), UpdateInput{
						ActorSystemAccountID: "sys_owner",
						RouteStrategyID:      "route_1",
						HasMode:              true,
						Mode:                 targetMode,
					})
					if err != nil {
						t.Fatalf("Update() error = %v", err)
					}
					if store.updateInputs[0].ConfigJSON != nil ||
						result.RouteStrategy.NormalRoutingConfig != nil ||
						result.RouteStrategy.HybridRoutingConfig != nil {
						t.Fatalf("update input=%+v after=%+v", store.updateInputs[0], result.RouteStrategy)
					}
					switch sourceMode {
					case port.PublicRouteStrategyModeNormal:
						if result.Before.NormalRoutingConfig == nil ||
							result.Before.NormalRoutingConfig.SchedulingPreference != "speed_first" {
							t.Fatalf("before=%+v", result.Before)
						}
					case port.PublicRouteStrategyModeHybridSmart:
						if result.Before.HybridRoutingConfig == nil ||
							result.Before.HybridRoutingConfig["qualityPreference"] != "quality_first" {
							t.Fatalf("before=%+v", result.Before)
						}
					}
				})
			}
		}
	})

	t.Run("non null sibling configs are rejected", func(t *testing.T) {
		tests := []UpdateInput{
			{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HybridRoutingConfig:  NewConfigInput(map[string]any{}, true),
			},
			{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasMode:              true,
				Mode:                 "hybrid_smart",
				NormalRoutingConfig:  NewConfigInput(map[string]any{}, true),
				HybridRoutingConfig:  NewConfigInput(validManagementHybridCreateConfig(), true),
			},
			{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasMode:              true,
				Mode:                 "weighted",
				NormalRoutingConfig:  NewConfigInput(map[string]any{}, true),
			},
		}
		for index, input := range tests {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)
			if _, err := service.Update(context.Background(), input); err == nil {
				t.Fatalf("case %d Update() error = nil", index)
			} else if _, ok := ValidationMessage(err); !ok {
				t.Fatalf("case %d Update() error = %T %v", index, err, err)
			}
		}
	})

	t.Run("malformed current config rejects every patch path", func(t *testing.T) {
		type patchCase struct {
			name   string
			mutate func(port.PublicRouteStrategyMode, *managementRouteStrategyUpdateStore, *UpdateInput)
		}
		patches := []patchCase{
			{
				name: "name only",
				mutate: func(_ port.PublicRouteStrategyMode, _ *managementRouteStrategyUpdateStore, input *UpdateInput) {
					input.HasName = true
					input.Name = "新名称"
				},
			},
			{
				name: "explicit null",
				mutate: func(mode port.PublicRouteStrategyMode, _ *managementRouteStrategyUpdateStore, input *UpdateInput) {
					if mode == port.PublicRouteStrategyModeNormal {
						input.NormalRoutingConfig = NewConfigInput(nil, true)
					} else {
						input.HybridRoutingConfig = NewConfigInput(nil, true)
					}
				},
			},
			{
				name: "explicit replacement",
				mutate: func(mode port.PublicRouteStrategyMode, _ *managementRouteStrategyUpdateStore, input *UpdateInput) {
					if mode == port.PublicRouteStrategyModeNormal {
						input.NormalRoutingConfig = NewConfigInput(map[string]any{
							"schedulingPreference": "speed_first",
							"speedFirstConfig": map[string]any{
								"slowTriggerCount": 5,
							},
						}, true)
					} else {
						input.HybridRoutingConfig = NewConfigInput(
							validManagementHybridCreateConfig(),
							true,
						)
					}
				},
			},
		}
		for _, targetMode := range []string{"weighted", "failover", "round_robin"} {
			patches = append(patches, patchCase{
				name: "switch to " + targetMode,
				mutate: func(
					_ port.PublicRouteStrategyMode,
					store *managementRouteStrategyUpdateStore,
					input *UpdateInput,
				) {
					input.HasMode = true
					input.Mode = targetMode
					if targetMode == "failover" {
						setUpdateStoreFailoverBindings(store)
					}
				},
			})
		}

		for _, mode := range []port.PublicRouteStrategyMode{
			port.PublicRouteStrategyModeNormal,
			port.PublicRouteStrategyModeHybridSmart,
		} {
			for _, patch := range patches {
				t.Run(string(mode)+"_"+patch.name, func(t *testing.T) {
					store := newManagementRouteStrategyUpdateStore("sys_owner")
					store.current.Mode = mode
					store.current.ConfigJSON = stringPointer(`{broken`)
					service, _, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)
					input := UpdateInput{
						ActorSystemAccountID: "sys_owner",
						RouteStrategyID:      "route_1",
					}
					patch.mutate(mode, store, &input)

					_, err := service.Update(context.Background(), input)
					message, ok := ValidationMessage(err)
					if !ok || !strings.Contains(message, "现有策略路由配置无效") {
						t.Fatalf("Update() error = %T %v, message=%q", err, err, message)
					}
					if len(store.updateInputs) != 0 || invalidator.calls != 0 {
						t.Fatalf(
							"update inputs=%d invalidation calls=%d, want 0",
							len(store.updateInputs),
							invalidator.calls,
						)
					}
				})
			}
		}
	})
}

func TestServiceUpdateRejectsMalformedCurrentConfigForNonConfigModes(t *testing.T) {
	for _, mode := range []port.PublicRouteStrategyMode{
		port.PublicRouteStrategyModeWeighted,
		port.PublicRouteStrategyModeFailover,
		port.PublicRouteStrategyModeRoundRobin,
	} {
		t.Run(string(mode), func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			store.current.Mode = mode
			store.current.ConfigJSON = stringPointer(`{broken`)
			if mode == port.PublicRouteStrategyModeFailover {
				setUpdateStoreFailoverBindings(store)
			}
			service, _, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

			_, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasName:              true,
				Name:                 "更新后",
			})
			message, ok := ValidationMessage(err)
			if !ok || !strings.Contains(message, "现有策略路由配置无效") {
				t.Fatalf("Update() error = %T %v, message=%q", err, err, message)
			}
			if len(store.updateInputs) != 0 || invalidator.calls != 0 {
				t.Fatalf(
					"update inputs=%d invalidation calls=%d, want 0",
					len(store.updateInputs),
					invalidator.calls,
				)
			}
		})
	}
}

func TestRouteStrategyUpdateCurrentConfigClearsParsedConfigForNonConfigModes(t *testing.T) {
	raw := `{"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":{"slowTriggerCount":4}}}`
	for _, mode := range []port.PublicRouteStrategyMode{
		port.PublicRouteStrategyModeWeighted,
		port.PublicRouteStrategyModeFailover,
		port.PublicRouteStrategyModeRoundRobin,
	} {
		t.Run(string(mode), func(t *testing.T) {
			config, err := routeStrategyUpdateCurrentConfig(port.PublicRouteStrategySummary{
				Mode:       mode,
				ConfigJSON: &raw,
			})
			if err != nil {
				t.Fatalf("routeStrategyUpdateCurrentConfig() error = %v", err)
			}
			if config.NormalRoutingConfig != nil || config.HybridRoutingConfig != nil {
				t.Fatalf("routeStrategyUpdateCurrentConfig() config = %+v, want empty", config)
			}
		})
	}
}

func TestServiceUpdateBindingPresenceAuthorizationAndDisabledGroups(t *testing.T) {
	t.Run("omitted bindings reuse current without authorization and regenerate ids", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.current.GroupBindings[0].GroupEnabled = false
		store.groups = map[string]port.PublicRouteStrategyBindableGroup{}
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasName:              true,
			Name:                 "名称",
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.bindableCalls != 1 ||
			store.updateInputs[0].Bindings[0].ID == "binding_old" ||
			result.RouteStrategy.GroupBindings[0].ID == "binding_old" {
			t.Fatalf("bindable calls=%d update bindings=%+v after=%+v", store.bindableCalls, store.updateInputs[0].Bindings, result.RouteStrategy.GroupBindings)
		}
	})

	t.Run("explicit authorized own or effective authorization groups replace all bindings", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.groups["group_authorized"] = port.PublicRouteStrategyBindableGroup{
			ID:              "group_authorized",
			SystemAccountID: "sys_source",
			Name:            "授权分组",
			ProviderCode:    "openai",
			Enabled:         true,
		}
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasMode:              true,
			Mode:                 "weighted",
			HasGroupBindings:     true,
			GroupBindings: []CreateGroupBindingInput{
				{GroupID: "group_1"},
				{GroupID: "group_authorized", Weight: 37},
			},
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if store.bindableCalls != 1 || len(result.RouteStrategy.GroupBindings) != 2 {
			t.Fatalf("bindable calls=%d bindings=%+v", store.bindableCalls, result.RouteStrategy.GroupBindings)
		}
	})

	t.Run("explicit unauthorized group is rejected", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

		_, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasGroupBindings:     true,
			GroupBindings:        []CreateGroupBindingInput{{GroupID: "group_missing"}},
		})
		message, ok := ValidationMessage(err)
		if !ok || !strings.Contains(message, "只能绑定自己的分组或有效授权给自己的分组") {
			t.Fatalf("Update() error = %T %v", err, err)
		}
	})

	t.Run("active disabled group is rejected but disabled binding is allowed", func(t *testing.T) {
		for _, status := range []string{"active", "disabled"} {
			t.Run(status, func(t *testing.T) {
				store := newManagementRouteStrategyUpdateStore("sys_owner")
				group := store.groups["group_2"]
				group.Enabled = false
				store.groups["group_2"] = group
				service, _, _, _ := newManagementRouteStrategyUpdateService(store, nil)

				_, err := service.Update(context.Background(), UpdateInput{
					ActorSystemAccountID: "sys_owner",
					RouteStrategyID:      "route_1",
					HasMode:              true,
					Mode:                 "weighted",
					HasGroupBindings:     true,
					GroupBindings: []CreateGroupBindingInput{
						{GroupID: "group_1"},
						{GroupID: "group_2", Status: status},
					},
				})
				if status == "active" {
					message, ok := ValidationMessage(err)
					if !ok || !strings.Contains(message, "不能启用已停用分组") {
						t.Fatalf("Update() error = %T %v", err, err)
					}
				} else if err != nil {
					t.Fatalf("Update() error = %v", err)
				}
			})
		}
	})
}

func TestServiceUpdateLocksDependenciesBeforeRouteForExplicitAndOmittedBindings(t *testing.T) {
	tests := []struct {
		name       string
		input      UpdateInput
		wantEvents []string
	}{
		{
			name: "explicit bindings",
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasMode:              true,
				Mode:                 "weighted",
				HasGroupBindings:     true,
				GroupBindings: []CreateGroupBindingInput{
					{GroupID: "group_2", Priority: 2},
					{GroupID: "group_1", Priority: 1},
				},
			},
			wantEvents: []string{"bindable-lock:group_1,group_2", "route-lock", "update"},
		},
		{
			name: "omitted bindings",
			input: UpdateInput{
				ActorSystemAccountID: "sys_owner",
				RouteStrategyID:      "route_1",
				HasName:              true,
				Name:                 "更新后",
			},
			wantEvents: []string{"bindable-lock:group_1", "route-lock", "update"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newManagementRouteStrategyUpdateStore("sys_owner")
			service, tx, _, _ := newManagementRouteStrategyUpdateService(store, nil)

			if _, err := service.Update(context.Background(), tt.input); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if tx.calls != 1 || tx.rollbacks != 0 || tx.commits != 1 {
				t.Fatalf("transaction calls=%d rollbacks=%d commits=%d", tx.calls, tx.rollbacks, tx.commits)
			}
			if !reflect.DeepEqual(store.txEvents, tt.wantEvents) {
				t.Fatalf("transaction events = %#v, want %#v", store.txEvents, tt.wantEvents)
			}
			if store.readCurrentFindCalls != 1 || store.txCurrentFindCalls != 1 {
				t.Fatalf(
					"route reads=%d locks=%d, want 1 each",
					store.readCurrentFindCalls,
					store.txCurrentFindCalls,
				)
			}
		})
	}
}

func TestServiceUpdateRetriesOmittedBindingsWithLatestGroupIDs(t *testing.T) {
	store := newManagementRouteStrategyUpdateStore("sys_owner")
	store.groups = map[string]port.PublicRouteStrategyBindableGroup{}
	latest := store.current
	latest.GroupBindings = []port.PublicRouteStrategyGroupBindingSummary{{
		ID:           "binding_concurrent",
		GroupID:      "group_2",
		GroupName:    "并发分组",
		ProviderCode: "openai",
		Priority:     1,
		Weight:       25,
		Status:       port.PublicRouteStrategyStatusActive,
		GroupEnabled: false,
	}}
	store.txCurrentSnapshots = []port.PublicRouteStrategySummary{latest, latest}
	service, tx, _, _ := newManagementRouteStrategyUpdateService(store, nil)

	result, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_owner",
		RouteStrategyID:      "route_1",
		HasName:              true,
		Name:                 "更新后",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if tx.calls != 2 || tx.rollbacks != 1 || tx.commits != 1 {
		t.Fatalf("transaction calls=%d rollbacks=%d commits=%d", tx.calls, tx.rollbacks, tx.commits)
	}
	wantLockIDs := [][]string{{"group_1"}, {"group_2"}}
	if !reflect.DeepEqual(store.txBindableGroupIDs, wantLockIDs) {
		t.Fatalf("bindable lock ids = %#v, want %#v", store.txBindableGroupIDs, wantLockIDs)
	}
	if len(store.updateInputs) != 1 ||
		len(store.updateInputs[0].Bindings) != 1 ||
		store.updateInputs[0].Bindings[0].GroupID != "group_2" ||
		store.updateInputs[0].Bindings[0].ID == "binding_concurrent" {
		t.Fatalf("update inputs = %+v", store.updateInputs)
	}
	if result.Before.GroupBindings[0].GroupID != "group_2" ||
		result.RouteStrategy.GroupBindings[0].GroupID != "group_2" ||
		result.RouteStrategy.GroupBindings[0].ID == "binding_concurrent" {
		t.Fatalf("before=%+v after=%+v", result.Before.GroupBindings, result.RouteStrategy.GroupBindings)
	}
}

func TestServiceUpdateExhaustsThreeTotalSnapshotAttempts(t *testing.T) {
	store := newManagementRouteStrategyUpdateStore("sys_owner")
	snapshot := func(groupID string) port.PublicRouteStrategySummary {
		current := store.current
		current.GroupBindings = []port.PublicRouteStrategyGroupBindingSummary{{
			ID:           "binding_" + groupID,
			GroupID:      groupID,
			GroupName:    "并发分组",
			ProviderCode: "openai",
			Priority:     1,
			Weight:       1,
			Status:       port.PublicRouteStrategyStatusActive,
			GroupEnabled: true,
		}}
		return current
	}
	store.txCurrentSnapshots = []port.PublicRouteStrategySummary{
		snapshot("group_2"),
		snapshot("group_3"),
		snapshot("group_4"),
	}
	service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

	result, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_owner",
		RouteStrategyID:      "route_1",
		HasName:              true,
		Name:                 "更新后",
	})
	var snapshotChanged *routeStrategyBindingSnapshotChangedError
	if !errors.As(err, &snapshotChanged) {
		t.Fatalf("Update() error = %T %v, want snapshot changed", err, err)
	}
	if !reflect.DeepEqual(snapshotChanged.groupIDs, []string{"group_4"}) {
		t.Fatalf("snapshot changed group ids = %#v", snapshotChanged.groupIDs)
	}
	if tx.calls != 3 || tx.rollbacks != 3 || tx.commits != 0 {
		t.Fatalf("transaction calls=%d rollbacks=%d commits=%d", tx.calls, tx.rollbacks, tx.commits)
	}
	if len(store.updateInputs) != 0 || invalidator.calls != 0 {
		t.Fatalf("updates=%d invalidations=%d", len(store.updateInputs), invalidator.calls)
	}
	if !reflect.DeepEqual(result, UpdateResult{}) {
		t.Fatalf("result = %+v, want zero", result)
	}
}

func TestServiceUpdateDoesNotRetryDeadlockErrors(t *testing.T) {
	store := newManagementRouteStrategyUpdateStore("sys_owner")
	deadlockErr := &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	store.bindableErr = deadlockErr
	service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

	_, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_owner",
		RouteStrategyID:      "route_1",
		HasName:              true,
		Name:                 "更新后",
	})
	if !errors.Is(err, deadlockErr) {
		t.Fatalf("Update() error = %v, want deadlock", err)
	}
	if tx.calls != 1 || tx.rollbacks != 1 || len(store.updateInputs) != 0 {
		t.Fatalf(
			"transaction calls=%d rollbacks=%d updates=%d",
			tx.calls,
			tx.rollbacks,
			len(store.updateInputs),
		)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceUpdateMapsDuplicateAndTransactionFailures(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		store.updateErr = port.ErrPublicRouteStrategyDuplicateName
		service, _, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)

		_, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasName:              true,
			Name:                 " 重复策略 ",
		})
		message, ok := NameExistsMessage(err)
		if !ok || message != "策略路由名称已存在：重复策略" {
			t.Fatalf("Update() error = %T %v, message=%q", err, err, message)
		}
		if invalidator.calls != 0 {
			t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
		}
	})

	t.Run("commit failure does not invalidate", func(t *testing.T) {
		store := newManagementRouteStrategyUpdateStore("sys_owner")
		service, tx, invalidator, _ := newManagementRouteStrategyUpdateService(store, nil)
		commitErr := errors.New("commit failed")
		tx.afterErr = commitErr

		_, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_owner",
			RouteStrategyID:      "route_1",
			HasName:              true,
			Name:                 "名称",
		})
		if !errors.Is(err, commitErr) {
			t.Fatalf("Update() error = %v, want %v", err, commitErr)
		}
		if invalidator.calls != 0 {
			t.Fatalf("invalidation calls = %d, want 0", invalidator.calls)
		}
	})
}

func TestServiceUpdateInvalidationIsDetachedBestEffort(t *testing.T) {
	store := newManagementRouteStrategyUpdateStore("sys_owner")
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	service, _, invalidator, _ := newManagementRouteStrategyUpdateService(store, logger)
	invalidator.err = errors.New("invalidate failed")
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Update(parent, UpdateInput{
		ActorSystemAccountID: "sys_owner",
		RouteStrategyID:      "route_1",
		HasName:              true,
		Name:                 "名称",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.RouteStrategy.ID == "" ||
		invalidator.calls != 1 ||
		invalidator.contextErr != nil ||
		invalidator.deadlineRemaining <= 0 ||
		invalidator.deadlineRemaining > 5*time.Second {
		t.Fatalf("result=%+v invalidator=%+v", result.RouteStrategy, invalidator)
	}
	if !strings.Contains(logs.String(), "策略路由更新后网关运行态失效失败") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func hybridUpdateConfigJSON(t *testing.T) *string {
	t.Helper()
	config, configJSON, err := normalizeCreateConfig(
		"hybrid_smart",
		ConfigInput{},
		NewConfigInput(validManagementHybridCreateConfig(), true),
	)
	if err != nil || config.HybridRoutingConfig == nil || configJSON == nil {
		t.Fatalf("normalize hybrid config error=%v config=%+v json=%v", err, config, configJSON)
	}
	return configJSON
}

func setUpdateStoreFailoverBindings(store *managementRouteStrategyUpdateStore) {
	store.current.GroupBindings = []port.PublicRouteStrategyGroupBindingSummary{
		{
			ID:           "binding_primary",
			GroupID:      "group_1",
			GroupName:    "分组一",
			ProviderCode: "openai",
			Priority:     1,
			Weight:       1,
			Status:       port.PublicRouteStrategyStatusActive,
			GroupEnabled: true,
		},
		{
			ID:           "binding_backup",
			GroupID:      "group_2",
			GroupName:    "分组二",
			ProviderCode: "openai",
			Priority:     2,
			Weight:       1,
			Status:       port.PublicRouteStrategyStatusActive,
			GroupEnabled: true,
		},
	}
}

func newManagementRouteStrategyUpdateService(
	store *managementRouteStrategyUpdateStore,
	logger *slog.Logger,
) (*Service, *managementRouteStrategyUpdateTransactor, *managementRouteStrategyInvalidator, *bytes.Buffer) {
	tx := &managementRouteStrategyUpdateTransactor{store: store}
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
		Now:         func() time.Time { return managementRouteStrategyUpdateTestNow },
		NewID: func(prefix string) string {
			sequence++
			return prefix + "_update_" + string(rune('0'+sequence))
		},
	})
	return service, tx, invalidator, &logs
}

type managementRouteStrategyUpdateTransactor struct {
	store     port.PublicRouteStrategyStore
	calls     int
	rollbacks int
	commits   int
	beforeErr error
	afterErr  error
}

type managementRouteStrategyUpdateTxContextKey struct{}

func (t *managementRouteStrategyUpdateTransactor) PublicRouteStrategyInTx(
	ctx context.Context,
	fn func(context.Context, port.PublicRouteStrategyStore) error,
) error {
	t.calls++
	if t.beforeErr != nil {
		return t.beforeErr
	}
	txCtx := context.WithValue(ctx, managementRouteStrategyUpdateTxContextKey{}, true)
	if err := fn(txCtx, t.store); err != nil {
		t.rollbacks++
		return err
	}
	if t.afterErr != nil {
		t.rollbacks++
		return t.afterErr
	}
	t.commits++
	return nil
}

type managementRouteStrategyUpdateStore struct {
	current              port.PublicRouteStrategySummary
	currentFound         bool
	currentErr           error
	currentFindCalls     int
	readCurrentFindCalls int
	txCurrentFindCalls   int
	txCurrentSnapshots   []port.PublicRouteStrategySummary
	target               port.PublicGroupTarget
	targetFound          bool
	targetErr            error
	targetFindCalls      int
	groups               map[string]port.PublicRouteStrategyBindableGroup
	bindableCalls        int
	bindableErr          error
	updateInputs         []port.PublicRouteStrategyUpdateInput
	updateFound          bool
	updateErr            error
	originalConfigJSON   *string
	txEvents             []string
	txBindableGroupIDs   [][]string
}

func newManagementRouteStrategyUpdateStore(ownerID string) *managementRouteStrategyUpdateStore {
	configJSON := `{"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":{"slowTriggerCount":4}}}`
	description := "原说明"
	current := port.PublicRouteStrategySummary{
		ID:              "route_1",
		SystemAccountID: ownerID,
		Name:            "原策略",
		Description:     &description,
		Mode:            port.PublicRouteStrategyModeNormal,
		Status:          port.PublicRouteStrategyStatusActive,
		IsDefault:       true,
		ConfigJSON:      &configJSON,
		GroupBindings: []port.PublicRouteStrategyGroupBindingSummary{{
			ID:           "binding_old",
			GroupID:      "group_1",
			GroupName:    "分组一",
			ProviderCode: "openai",
			Priority:     1,
			Weight:       1,
			Status:       port.PublicRouteStrategyStatusActive,
			GroupEnabled: true,
		}},
		APIKeyCount: 7,
		CreatedAt:   managementRouteStrategyUpdateTestNow.Add(-48 * time.Hour),
		UpdatedAt:   managementRouteStrategyUpdateTestNow.Add(-24 * time.Hour),
	}
	return &managementRouteStrategyUpdateStore{
		current:      current,
		currentFound: true,
		target: port.PublicGroupTarget{
			ID:          ownerID,
			DisplayName: "停用所有者",
			Status:      "disabled",
		},
		targetFound: true,
		groups: map[string]port.PublicRouteStrategyBindableGroup{
			"group_1": {
				ID:              "group_1",
				SystemAccountID: ownerID,
				Name:            "分组一",
				ProviderCode:    "openai",
				Enabled:         true,
			},
			"group_2": {
				ID:              "group_2",
				SystemAccountID: ownerID,
				Name:            "分组二",
				ProviderCode:    "openai",
				Enabled:         true,
			},
		},
		updateFound:        true,
		originalConfigJSON: &configJSON,
	}
}

func (s *managementRouteStrategyUpdateStore) FindPublicRouteStrategyTargetByUsername(
	context.Context,
	string,
) (port.PublicGroupTarget, bool, error) {
	return port.PublicGroupTarget{}, false, errors.New("unexpected username target lookup")
}

func (s *managementRouteStrategyUpdateStore) FindPublicRouteStrategyTargetByID(
	_ context.Context,
	id string,
) (port.PublicGroupTarget, bool, error) {
	s.targetFindCalls++
	if s.targetErr != nil {
		return port.PublicGroupTarget{}, false, s.targetErr
	}
	if !s.targetFound || s.target.ID != id {
		return port.PublicGroupTarget{}, false, nil
	}
	return s.target, true, nil
}

func (s *managementRouteStrategyUpdateStore) ListPublicRouteStrategies(
	context.Context,
	port.PublicRouteStrategyListInput,
) (port.PublicRouteStrategyListPage, error) {
	return port.PublicRouteStrategyListPage{}, errors.New("unexpected route strategy list")
}

func (s *managementRouteStrategyUpdateStore) FindPublicRouteStrategyByID(
	ctx context.Context,
	id string,
) (port.PublicRouteStrategySummary, bool, error) {
	s.currentFindCalls++
	if managementRouteStrategyUpdateInTx(ctx) {
		s.txCurrentFindCalls++
		s.txEvents = append(s.txEvents, "route-lock")
		if len(s.txCurrentSnapshots) >= s.txCurrentFindCalls {
			s.current = cloneManagementRouteStrategySummary(
				s.txCurrentSnapshots[s.txCurrentFindCalls-1],
			)
		}
	} else {
		s.readCurrentFindCalls++
	}
	if s.currentErr != nil {
		return port.PublicRouteStrategySummary{}, false, s.currentErr
	}
	if !s.currentFound || s.current.ID != id {
		return port.PublicRouteStrategySummary{}, false, nil
	}
	return s.current, true, nil
}

func (s *managementRouteStrategyUpdateStore) FindPublicRouteStrategyBindableGroups(
	ctx context.Context,
	_ string,
	groupIDs []string,
) ([]port.PublicRouteStrategyBindableGroup, error) {
	s.bindableCalls++
	if managementRouteStrategyUpdateInTx(ctx) {
		ids := append([]string(nil), groupIDs...)
		s.txBindableGroupIDs = append(s.txBindableGroupIDs, ids)
		s.txEvents = append(s.txEvents, "bindable-lock:"+strings.Join(ids, ","))
	}
	if s.bindableErr != nil {
		return nil, s.bindableErr
	}
	groups := make([]port.PublicRouteStrategyBindableGroup, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if group, ok := s.groups[groupID]; ok {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

func (s *managementRouteStrategyUpdateStore) CreatePublicRouteStrategy(
	context.Context,
	port.PublicRouteStrategyCreateInput,
) (port.PublicRouteStrategySummary, error) {
	return port.PublicRouteStrategySummary{}, errors.New("unexpected route strategy create")
}

func (s *managementRouteStrategyUpdateStore) UpdatePublicRouteStrategy(
	ctx context.Context,
	input port.PublicRouteStrategyUpdateInput,
) (port.PublicRouteStrategySummary, bool, error) {
	if managementRouteStrategyUpdateInTx(ctx) {
		s.txEvents = append(s.txEvents, "update")
	}
	s.updateInputs = append(s.updateInputs, input)
	if s.updateErr != nil {
		return port.PublicRouteStrategySummary{}, false, s.updateErr
	}
	if !s.updateFound {
		return port.PublicRouteStrategySummary{}, false, nil
	}
	updated := s.current
	updated.Name = input.Name
	updated.Description = input.Description
	updated.Mode = input.Mode
	updated.Status = input.Status
	updated.ConfigJSON = input.ConfigJSON
	updated.UpdatedAt = input.Now
	updated.GroupBindings = make([]port.PublicRouteStrategyGroupBindingSummary, 0, len(input.Bindings))
	for _, binding := range input.Bindings {
		group, ok := s.groups[binding.GroupID]
		if !ok {
			for _, currentBinding := range s.current.GroupBindings {
				if currentBinding.GroupID == binding.GroupID {
					group = port.PublicRouteStrategyBindableGroup{
						ID:              currentBinding.GroupID,
						SystemAccountID: s.current.SystemAccountID,
						Name:            currentBinding.GroupName,
						ProviderCode:    currentBinding.ProviderCode,
						Enabled:         currentBinding.GroupEnabled,
					}
					break
				}
			}
		}
		updated.GroupBindings = append(updated.GroupBindings, port.PublicRouteStrategyGroupBindingSummary{
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
	s.current = updated
	return updated, true, nil
}

func (s *managementRouteStrategyUpdateStore) DeletePublicRouteStrategy(
	context.Context,
	string,
	string,
) (bool, error) {
	return false, errors.New("unexpected route strategy delete")
}

func (s *managementRouteStrategyUpdateStore) PublicRouteStrategyAPIKeyCount(
	context.Context,
	string,
	string,
) (int64, error) {
	return 0, errors.New("unexpected api key count")
}

func managementRouteStrategyUpdateInTx(ctx context.Context) bool {
	inTx, _ := ctx.Value(managementRouteStrategyUpdateTxContextKey{}).(bool)
	return inTx
}

func cloneManagementRouteStrategySummary(
	summary port.PublicRouteStrategySummary,
) port.PublicRouteStrategySummary {
	summary.GroupBindings = append(
		[]port.PublicRouteStrategyGroupBindingSummary(nil),
		summary.GroupBindings...,
	)
	return summary
}
