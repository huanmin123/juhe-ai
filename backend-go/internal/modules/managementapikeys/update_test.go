package managementapikeys

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceUpdateScopesOwnerAndForwardsSparsePatch(t *testing.T) {
	tests := []struct {
		name             string
		input            UpdateInput
		wantOwner        string
		wantIncludeOwner bool
	}{
		{
			name: "admin global when target omitted",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
			},
			wantIncludeOwner: true,
		},
		{
			name: "admin global when target all",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "super_admin",
				SystemAccountID:      " all ",
			},
			wantIncludeOwner: true,
		},
		{
			name: "admin explicit owner",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      " sys_target ",
			},
			wantOwner:        "sys_target",
			wantIncludeOwner: true,
		},
		{
			name: "self only forces actor",
			input: UpdateInput{
				ActorSystemAccountID: " sys_self ",
				ActorRole:            "admin",
				SystemAccountID:      "sys_forged",
				SelfOnly:             true,
			},
			wantOwner: "sys_self",
		},
		{
			name: "non admin forces actor",
			input: UpdateInput{
				ActorSystemAccountID: "sys_user",
				ActorRole:            "user",
				SystemAccountID:      "sys_forged",
			},
			wantOwner: "sys_user",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			store := newManagementAPIKeyUpdateStoreStub(&events)
			invalidator := &managementAPIKeyInvalidatorStub{events: &events}
			service := NewServiceWithOptions(ServiceOptions{
				ListReader:  store,
				Updater:     store,
				Invalidator: invalidator,
				Now: func() time.Time {
					return time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
				},
			})
			input := test.input
			input.APIKeyID = " key_1 "
			input.HasName = true
			input.Name = " 新名称 "

			result, err := service.Update(context.Background(), input)
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if store.updateInput.APIKeyID != "key_1" ||
				store.updateInput.OwnerSystemAccountID != test.wantOwner ||
				!store.updateInput.HasName ||
				store.updateInput.Name != "新名称" ||
				store.updateInput.HasDescription ||
				store.updateInput.HasStatus ||
				store.updateInput.HasQuotaLimits {
				t.Fatalf("update input = %+v", store.updateInput)
			}
			if result.OwnerSystemAccountID != "sys_owner" ||
				!result.Committed ||
				result.Before.Name != "旧名称" ||
				result.After.Name != "新名称" ||
				result.After.Usage.RequestCount != 9 {
				t.Fatalf("result = %+v", result)
			}
			if test.wantIncludeOwner {
				if result.After.SystemAccountID != "sys_owner" ||
					result.After.SystemAccountName != "所有者" {
					t.Fatalf("admin result = %+v", result.After)
				}
			} else if result.After.SystemAccountID != "" ||
				result.After.SystemAccountName != "" {
				t.Fatalf("self result leaked owner: %+v", result.After)
			}
			if invalidator.runtimeReason != apiKeyUpdatedReason ||
				invalidator.quotaReason != apiKeyUpdatedReason ||
				invalidator.quotaAPIKeyID != "key_1" {
				t.Fatalf("invalidator = %+v", invalidator)
			}
		})
	}
}

func TestServiceUpdateRejectsInvalidScopeAndEmptyPatch(t *testing.T) {
	tests := []struct {
		name      string
		input     UpdateInput
		wantError string
	}{
		{
			name: "missing actor",
			input: UpdateInput{
				APIKeyID: "key_1",
				HasName:  true,
				Name:     "名称",
			},
			wantError: "API Key 更新参数无效",
		},
		{
			name: "missing api key id",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				HasName:              true,
				Name:                 "名称",
			},
			wantError: "API Key 更新参数无效",
		},
		{
			name: "empty patch",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				APIKeyID:             "key_1",
			},
			wantError: "请提供要修改的 API Key 内容",
		},
		{
			name: "blank name",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				APIKeyID:             "key_1",
				HasName:              true,
				Name:                 " ",
			},
			wantError: "API Key 名称不能为空",
		},
		{
			name: "blank status",
			input: UpdateInput{
				ActorSystemAccountID: "sys_admin",
				APIKeyID:             "key_1",
				HasStatus:            true,
				Status:               " ",
			},
			wantError: "API Key 状态无效",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newManagementAPIKeyUpdateStoreStub(nil)
			service := NewServiceWithOptions(ServiceOptions{
				ListReader:  store,
				Updater:     store,
				Invalidator: &managementAPIKeyInvalidatorStub{},
			})
			_, err := service.Update(context.Background(), test.input)
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("Update() error = %v, want %q", err, test.wantError)
			}
			if !IsAPIKeyUpdateValidationError(err) {
				t.Fatalf("Update() error = %T %v, want typed validation", err, err)
			}
			if store.updateCalls != 0 {
				t.Fatalf("update calls = %d, want 0", store.updateCalls)
			}
		})
	}
}

func TestServiceUpdateNormalizesNullableValuesAndSharedRules(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 30, 0, 0, time.UTC)
	store := newManagementAPIKeyUpdateStoreStub(nil)
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:               store,
		Updater:                  store,
		UsageStatsTimezoneReader: store,
		Invalidator:              &managementAPIKeyInvalidatorStub{},
		Now:                      func() time.Time { return now },
	})

	_, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID:    "sys_admin",
		ActorRole:               "admin",
		APIKeyID:                "key_1",
		HasDescription:          true,
		Description:             nil,
		HasExpiresAt:            true,
		ExpiresAt:               "",
		HasQuotaLimits:          true,
		QuotaLimits:             map[string]any{},
		HasAvailabilitySchedule: true,
		AvailabilitySchedule:    nil,
	})
	if err != nil {
		t.Fatalf("Update(clear nullable fields) error = %v", err)
	}
	if !store.updateInput.HasDescription || store.updateInput.Description != nil ||
		!store.updateInput.HasExpiresAt || store.updateInput.ExpiresAt != nil ||
		!store.updateInput.HasQuotaLimits || store.updateInput.QuotaLimitsJSON != nil ||
		store.updateInput.HourlyQuotaHours != nil ||
		!store.updateInput.HasAvailabilitySchedule ||
		store.updateInput.AvailabilityScheduleJSON != nil ||
		store.updateInput.AvailabilityScheduleNextCheckAt != nil ||
		store.updateInput.HasStatus {
		t.Fatalf("clear update input = %+v", store.updateInput)
	}

	store.resetUpdate()
	schedule := map[string]any{
		"enabled": true,
		"mode":    "allow_windows",
		"windows": []any{map[string]any{
			"daysOfWeek": []any{json.Number("1")},
			"start":      "08:00",
			"end":        "10:00",
		}},
	}
	_, err = service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "key_1",
		HasDescription:       true,
		Description:          "  新说明  ",
		HasStatus:            true,
		Status:               "disabled",
		HasExpiresAt:         true,
		ExpiresAt:            "2020-01-01T00:00:00.123Z",
		HasQuotaLimits:       true,
		QuotaLimits: map[string]any{
			"hourly": map[string]any{
				"enabled": true,
				"hours":   json.Number("6"),
				"limit":   json.Number("1.000001"),
			},
			"daily": map[string]any{
				"enabled": true,
				"limit":   json.Number("9.007199254740991e15"),
			},
		},
		HasAvailabilitySchedule: true,
		AvailabilitySchedule:    schedule,
	})
	if err != nil {
		t.Fatalf("Update(values) error = %v", err)
	}
	if store.updateInput.Description == nil ||
		*store.updateInput.Description != "新说明" ||
		store.updateInput.ExpiresAt == nil ||
		store.updateInput.ExpiresAt.Year() != 2020 ||
		store.updateInput.QuotaLimitsJSON == nil ||
		!strings.Contains(*store.updateInput.QuotaLimitsJSON, "9.007199254740991e15") ||
		store.updateInput.HourlyQuotaHours == nil ||
		*store.updateInput.HourlyQuotaHours != 6 ||
		store.updateInput.AvailabilityScheduleJSON == nil ||
		store.updateInput.AvailabilityScheduleNextCheckAt == nil ||
		!store.updateInput.HasStatus ||
		store.updateInput.Status != "active" {
		t.Fatalf("value update input = %+v", store.updateInput)
	}

	for _, input := range []UpdateInput{
		{
			ActorSystemAccountID: "sys_admin",
			APIKeyID:             "key_1",
			HasDescription:       true,
			Description:          strings.Repeat("😀", 101),
		},
		{
			ActorSystemAccountID: "sys_admin",
			APIKeyID:             "key_1",
			HasExpiresAt:         true,
			ExpiresAt:            "2026-07-31T23:59:58.1Z",
		},
		{
			ActorSystemAccountID: "sys_admin",
			APIKeyID:             "key_1",
			HasQuotaLimits:       true,
			QuotaLimits: map[string]any{
				"daily": map[string]any{
					"enabled": true,
					"limit":   json.Number("9007199254740992"),
				},
			},
		},
		{
			ActorSystemAccountID: "sys_admin",
			APIKeyID:             "key_1",
			HasQuotaLimits:       true,
			QuotaLimits: map[string]any{
				"daily": map[string]any{
					"enabled": true,
					"limit":   json.Number("1.0000001"),
				},
			},
		},
	} {
		store.resetUpdate()
		if _, err := service.Update(context.Background(), input); err == nil ||
			!IsAPIKeyUpdateValidationError(err) {
			t.Fatalf("Update(%+v) error = %v", input, err)
		}
		if store.updateCalls != 0 {
			t.Fatalf("invalid input wrote store: %+v", store.updateInput)
		}
	}
}

func TestServiceUpdateMapsStoreErrors(t *testing.T) {
	tests := []struct {
		name     string
		storeErr error
		wantErr  error
		wantText string
	}{
		{
			name:     "not found",
			storeErr: port.ErrManagementAPIKeyNotFound,
			wantErr:  ErrAPIKeyNotFound,
		},
		{
			name:     "route missing",
			storeErr: port.ErrManagementAPIKeyRouteStrategyNotFound,
			wantErr:  ErrAPIKeyRouteStrategyMissing,
		},
		{
			name:     "route disabled",
			storeErr: port.ErrManagementAPIKeyRouteStrategyDisabled,
			wantErr:  ErrAPIKeyRouteStrategyOff,
		},
		{
			name:     "default route change",
			storeErr: port.ErrManagementAPIKeyDefaultRouteChange,
			wantErr:  ErrAPIKeyDefaultRouteChange,
			wantText: "默认 API Key 不允许更换策略路由",
		},
		{
			name:     "duplicate name",
			storeErr: port.ErrManagementAPIKeyNameExists,
			wantText: "API Key 名称已存在：新名称",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newManagementAPIKeyUpdateStoreStub(nil)
			store.updateErr = test.storeErr
			service := NewServiceWithOptions(ServiceOptions{
				ListReader:  store,
				Updater:     store,
				Invalidator: &managementAPIKeyInvalidatorStub{},
			})
			_, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: "sys_admin",
				APIKeyID:             "key_1",
				HasName:              true,
				Name:                 "新名称",
			})
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, test.wantErr)
			}
			if test.wantText != "" && (err == nil || err.Error() != test.wantText) {
				t.Fatalf("Update() error = %v, want %q", err, test.wantText)
			}
		})
	}
}

func TestServiceUpdateValidationFailureReturnsCommittedResultWithBoundedLiveContext(t *testing.T) {
	events := []string{}
	ctx, cancel := context.WithCancel(context.Background())
	store := newManagementAPIKeyUpdateStoreStub(&events)
	store.afterUpdate = cancel
	store.respectUsageContext = true
	validationErr := errors.New("validation unavailable")
	invalidator := &managementAPIKeyInvalidatorStub{
		events:        &events,
		validationErr: validationErr,
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:  store,
		Updater:     store,
		Invalidator: invalidator,
	})

	startedAt := time.Now()
	result, err := service.Update(ctx, UpdateInput{
		ActorSystemAccountID: "sys_admin",
		APIKeyID:             "key_1",
		HasName:              true,
		Name:                 "新名称",
	})
	if !errors.Is(err, ErrAPIKeyUpdateValidationCacheInvalidation) {
		t.Fatalf(
			"Update() error = %v, want %v",
			err,
			ErrAPIKeyUpdateValidationCacheInvalidation,
		)
	}
	if errors.Is(err, validationErr) {
		t.Fatalf("Update() error leaked validation cause: %v", err)
	}
	if err.Error() != ErrAPIKeyUpdateValidationCacheInvalidation.Error() {
		t.Fatalf(
			"Update() error text = %q, want stable %q",
			err.Error(),
			ErrAPIKeyUpdateValidationCacheInvalidation.Error(),
		)
	}
	if !result.Committed ||
		result.Before.ID != "key_1" ||
		result.After.Name != "新名称" ||
		result.OwnerSystemAccountID != "sys_owner" ||
		result.Before.Usage.RequestCount != 9 ||
		result.After.Usage.RequestCount != 9 {
		t.Fatalf("result = %+v", result)
	}
	if got, want := events, []string{"update", "usage", "validation"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("original context error = %v, want context canceled", ctx.Err())
	}
	if invalidator.validationContextErr != nil ||
		!invalidator.validationContextHasDeadline ||
		invalidator.calls != 1 ||
		store.usageCalls != 1 ||
		store.usageContextErr != nil ||
		!store.usageContextHasDeadline ||
		!store.usageContextDeadline.After(startedAt) ||
		store.usageContextDeadline.After(startedAt.Add(5*time.Second+250*time.Millisecond)) {
		t.Fatalf("invalidator=%+v store=%+v", invalidator, store)
	}
}

func TestServiceUpdateRuntimeQuotaAreBestEffortAndUsageFailureIsCommitted(t *testing.T) {
	t.Run("runtime and quota failures do not replace success", func(t *testing.T) {
		store := newManagementAPIKeyUpdateStoreStub(nil)
		scheduleJSON := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"08:00","end":"10:00"}]}`
		store.result.Before.AvailabilityScheduleJSON = &scheduleJSON
		store.result.After.AvailabilityScheduleJSON = &scheduleJSON
		invalidator := &managementAPIKeyInvalidatorStub{
			runtimeErr: errors.New("runtime unavailable"),
			quotaErr:   errors.New("quota unavailable"),
		}
		service := NewServiceWithOptions(ServiceOptions{
			ListReader:  store,
			Updater:     store,
			Invalidator: invalidator,
		})
		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_admin",
			APIKeyID:             "key_1",
			HasStatus:            true,
			Status:               "disabled",
		})
		if err != nil ||
			!result.Committed ||
			invalidator.calls != 3 ||
			!store.updateInput.HasStatus ||
			store.updateInput.HasAvailabilitySchedule ||
			result.After.AvailabilitySchedule == nil {
			t.Fatalf("result=%+v err=%v invalidator=%+v", result, err, invalidator)
		}
	})

	t.Run("usage read failure returns committed result and internal error", func(t *testing.T) {
		events := []string{}
		store := newManagementAPIKeyUpdateStoreStub(&events)
		usageErr := errors.New("usage summary unavailable")
		store.usageErr = usageErr
		invalidator := &managementAPIKeyInvalidatorStub{events: &events}
		service := NewServiceWithOptions(ServiceOptions{
			ListReader:  store,
			Updater:     store,
			Invalidator: invalidator,
		})
		result, err := service.Update(context.Background(), UpdateInput{
			ActorSystemAccountID: "sys_admin",
			APIKeyID:             "key_1",
			HasStatus:            true,
			Status:               "disabled",
		})
		if !errors.Is(err, usageErr) ||
			!result.Committed ||
			result.Before.ID != "key_1" ||
			result.After.Status != "disabled" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if got, want := events, []string{"update", "usage", "validation", "runtime", "quota"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("events = %v, want %v", got, want)
		}
		if invalidator.calls != 3 {
			t.Fatalf("invalidator calls = %d, want 3", invalidator.calls)
		}
	})
}

func TestServiceUpdateFailsFastWhenRequiredDependenciesAreMissing(t *testing.T) {
	store := newManagementAPIKeyUpdateStoreStub(nil)
	tests := []struct {
		name    string
		options ServiceOptions
	}{
		{
			name: "updater",
			options: ServiceOptions{
				ListReader:  store,
				Invalidator: &managementAPIKeyInvalidatorStub{},
			},
		},
		{
			name: "usage reader",
			options: ServiceOptions{
				Updater:     store,
				Invalidator: &managementAPIKeyInvalidatorStub{},
			},
		},
		{
			name: "validation invalidator",
			options: ServiceOptions{
				ListReader: store,
				Updater:    store,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store.resetUpdate()
			service := NewServiceWithOptions(test.options)
			_, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: "sys_admin",
				APIKeyID:             "key_1",
				HasName:              true,
				Name:                 "新名称",
			})
			if err == nil {
				t.Fatal("Update() error = nil")
			}
			if store.updateCalls != 0 {
				t.Fatalf("update calls = %d, want 0", store.updateCalls)
			}
		})
	}
}

type managementAPIKeyUpdateStoreStub struct {
	updateInput port.ManagementAPIKeyUpdateInput
	result      port.ManagementAPIKeyUpdateResult
	usage       []port.ManagementAPIKeyUsageRow
	updateErr   error
	usageErr    error
	timezone    string
	timezoneOK  bool
	timezoneErr error
	events      *[]string
	afterUpdate func()
	updateCalls int
	usageCalls  int

	respectUsageContext     bool
	usageContextErr         error
	usageContextDeadline    time.Time
	usageContextHasDeadline bool
}

func newManagementAPIKeyUpdateStoreStub(events *[]string) *managementAPIKeyUpdateStoreStub {
	before := port.ManagementAPIKeyListRow{
		ID:                  "key_1",
		SystemAccountID:     "sys_owner",
		SystemAccountName:   "所有者",
		Name:                "旧名称",
		KeyPrefix:           "sk-before",
		KeySuffix:           "before",
		Status:              "active",
		RouteStrategyID:     "route_1",
		RouteStrategyName:   "默认策略",
		RouteStrategyMode:   "normal",
		RouteStrategyStatus: "active",
	}
	after := before
	after.Name = "新名称"
	return &managementAPIKeyUpdateStoreStub{
		result: port.ManagementAPIKeyUpdateResult{Before: before, After: after},
		usage: []port.ManagementAPIKeyUsageRow{{
			SystemAccountID: "sys_owner",
			APIKeyID:        "key_1",
			Usage: port.ManagementAccountUsageSummary{
				RequestCount: 9,
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
			},
		}},
		timezone:   "Asia/Shanghai",
		timezoneOK: true,
		events:     events,
	}
}

func (s *managementAPIKeyUpdateStoreStub) UpdateManagementAPIKey(
	_ context.Context,
	input port.ManagementAPIKeyUpdateInput,
) (port.ManagementAPIKeyUpdateResult, error) {
	s.updateCalls++
	s.updateInput = input
	s.record("update")
	if s.afterUpdate != nil {
		s.afterUpdate()
	}
	result := s.result
	if input.HasName {
		result.After.Name = input.Name
	}
	if input.HasDescription {
		result.After.Description = input.Description
	}
	if input.HasRouteStrategyID {
		result.After.RouteStrategyID = input.RouteStrategyID
	}
	if input.HasStatus {
		result.After.Status = input.Status
	}
	if input.HasExpiresAt {
		result.After.ExpiresAt = input.ExpiresAt
	}
	if input.HasQuotaLimits {
		result.After.QuotaLimitsJSON = input.QuotaLimitsJSON
	}
	if input.HasAvailabilitySchedule {
		result.After.AvailabilityScheduleJSON = input.AvailabilityScheduleJSON
	}
	return result, s.updateErr
}

func (s *managementAPIKeyUpdateStoreStub) ListManagementAPIKeys(
	context.Context,
	port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	return port.ManagementAPIKeyListPage{}, nil
}

func (s *managementAPIKeyUpdateStoreStub) ListManagementAPIKeyUsageTotals(
	ctx context.Context,
	scopes []port.ManagementAPIKeyUsageScope,
) ([]port.ManagementAPIKeyUsageRow, error) {
	s.usageCalls++
	s.record("usage")
	s.usageContextErr = ctx.Err()
	s.usageContextDeadline, s.usageContextHasDeadline = ctx.Deadline()
	if s.respectUsageContext {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	if len(scopes) != 1 ||
		scopes[0] != (port.ManagementAPIKeyUsageScope{
			SystemAccountID: "sys_owner",
			APIKeyID:        "key_1",
		}) {
		return nil, errors.New("unexpected usage scope")
	}
	return s.usage, s.usageErr
}

func (s *managementAPIKeyUpdateStoreStub) GetManagementUsageStatsTimezone(
	context.Context,
) (string, bool, error) {
	return s.timezone, s.timezoneOK, s.timezoneErr
}

func (s *managementAPIKeyUpdateStoreStub) resetUpdate() {
	s.updateInput = port.ManagementAPIKeyUpdateInput{}
	s.updateCalls = 0
	s.usageCalls = 0
}

func (s *managementAPIKeyUpdateStoreStub) record(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

var _ port.ManagementAPIKeyUpdater = (*managementAPIKeyUpdateStoreStub)(nil)
var _ port.ManagementAPIKeyListReader = (*managementAPIKeyUpdateStoreStub)(nil)
var _ port.ManagementUsageStatsTimezoneReader = (*managementAPIKeyUpdateStoreStub)(nil)
