package managementapikeys

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceCreateScopesOwnerAndReturnsOneTimeSecret(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 30, 0, 0, time.UTC)
	tests := []struct {
		name             string
		input            CreateInput
		wantOwner        string
		wantIncludeOwner bool
	}{
		{
			name: "admin target",
			input: CreateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      " sys_target ",
			},
			wantOwner:        "sys_target",
			wantIncludeOwner: true,
		},
		{
			name: "admin all falls back to actor",
			input: CreateInput{
				ActorSystemAccountID: " sys_admin ",
				ActorRole:            "super_admin",
				SystemAccountID:      " all ",
			},
			wantOwner:        "sys_admin",
			wantIncludeOwner: true,
		},
		{
			name: "admin empty target falls back to actor",
			input: CreateInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
			},
			wantOwner:        "sys_admin",
			wantIncludeOwner: true,
		},
		{
			name: "self only forces actor and hides owner",
			input: CreateInput{
				ActorSystemAccountID: "sys_self",
				ActorRole:            "admin",
				SystemAccountID:      "sys_forged",
				SelfOnly:             true,
			},
			wantOwner: "sys_self",
		},
		{
			name: "non admin forces actor and hides owner",
			input: CreateInput{
				ActorSystemAccountID: "sys_user",
				ActorRole:            "user",
				SystemAccountID:      "sys_forged",
			},
			wantOwner: "sys_user",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeyCreateStoreStub{}
			input := test.input
			input.Name = "  生产 Key  "
			input.Description = "  用于生产  "
			input.RouteStrategyID = " route_1 "
			service := newCreateTestService(store, createTestOptions{
				now:    now,
				secret: "sk-created-secret-0123456789",
				id:     "key_created",
			})

			result, err := service.Create(context.Background(), input)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if store.createCalls != 1 {
				t.Fatalf("create calls = %d, want 1", store.createCalls)
			}
			got := store.createInput
			if got.ID != "key_created" ||
				got.SystemAccountID != test.wantOwner ||
				got.Name != "生产 Key" ||
				got.Description == nil ||
				*got.Description != "用于生产" ||
				got.RouteStrategyID != "route_1" ||
				got.Status != "active" ||
				got.IsDefault ||
				got.CreatedAt != now ||
				got.UpdatedAt != now {
				t.Fatalf("create input = %+v", got)
			}
			if got.KeyHash != apikeysecret.Hash("sk-created-secret-0123456789") ||
				got.KeyPrefix != "sk-creat" ||
				got.KeySuffix != "23456789" ||
				got.KeySecretEncrypted == "" {
				t.Fatalf("credential input = %+v", got)
			}
			payload, err := secretcrypto.NewJSONCodec("management-api-key-create-test").
				DecryptJSON(got.KeySecretEncrypted)
			if err != nil || payload["key"] != "sk-created-secret-0123456789" {
				t.Fatalf("ciphertext payload = %#v, err = %v", payload, err)
			}
			if result.Key != "sk-created-secret-0123456789" ||
				result.OwnerSystemAccountID != test.wantOwner ||
				result.ID != "key_created" ||
				result.Name != "生产 Key" ||
				result.KeyPrefix != "sk-creat" ||
				result.KeySuffix != "23456789" ||
				result.IsDefault ||
				result.Usage != (port.ManagementAccountUsageSummary{}) {
				t.Fatalf("result = %+v", result)
			}
			if test.wantIncludeOwner {
				if result.SystemAccountID != test.wantOwner ||
					result.SystemAccountName != "Owner "+test.wantOwner {
					t.Fatalf("admin owner fields = %+v", result.ListItem)
				}
			} else if result.SystemAccountID != "" || result.SystemAccountName != "" {
				t.Fatalf("self result leaked owner fields: %+v", result.ListItem)
			}
		})
	}
}

func TestServiceCreateValidatesNameDescriptionStatusAndExpiresAt(t *testing.T) {
	validExpirySeconds := "2026-07-31T23:59:58Z"
	validExpiryMillis := "2026-07-31T23:59:58.123Z"
	tests := []struct {
		name        string
		mutate      func(*CreateInput)
		wantError   string
		wantExpires *time.Time
		wantStatus  string
	}{
		{
			name:      "missing actor",
			mutate:    func(input *CreateInput) { input.ActorSystemAccountID = " " },
			wantError: "创建参数无效",
		},
		{
			name:      "blank name",
			mutate:    func(input *CreateInput) { input.Name = " " },
			wantError: "API Key 名称不能为空",
		},
		{
			name:      "description wrong type",
			mutate:    func(input *CreateInput) { input.Description = 1 },
			wantError: "API Key 说明必须是字符串",
		},
		{
			name: "description too long",
			mutate: func(input *CreateInput) {
				input.Description = strings.Repeat("x", 201)
			},
			wantError: "API Key 说明不能超过 200 个字符",
		},
		{
			name: "description counts UTF-16 code units like Node",
			mutate: func(input *CreateInput) {
				input.Description = strings.Repeat("😀", 101)
			},
			wantError: "API Key 说明不能超过 200 个字符",
		},
		{
			name:      "invalid status",
			mutate:    func(input *CreateInput) { input.Status = "paused" },
			wantError: "API Key 状态无效",
		},
		{
			name:      "expires wrong type",
			mutate:    func(input *CreateInput) { input.ExpiresAt = 1 },
			wantError: "API Key 过期时间必须是有效时间字符串",
		},
		{
			name:      "expires offset rejected",
			mutate:    func(input *CreateInput) { input.ExpiresAt = "2026-07-31T23:59:58+08:00" },
			wantError: "API Key 过期时间必须是有效时间字符串",
		},
		{
			name:      "expires fractional width rejected",
			mutate:    func(input *CreateInput) { input.ExpiresAt = "2026-07-31T23:59:58.1Z" },
			wantError: "API Key 过期时间必须是有效时间字符串",
		},
		{
			name:      "expires impossible date rejected",
			mutate:    func(input *CreateInput) { input.ExpiresAt = "2026-02-29T00:00:00Z" },
			wantError: "API Key 过期时间必须是有效时间字符串",
		},
		{
			name:       "defaults and empty nullable fields",
			mutate:     func(input *CreateInput) { input.Description = " "; input.ExpiresAt = "" },
			wantStatus: "active",
		},
		{
			name: "disabled accepted",
			mutate: func(input *CreateInput) {
				input.Status = "disabled"
				input.Description = nil
				input.ExpiresAt = nil
			},
			wantStatus: "disabled",
		},
		{
			name: "seconds expiry",
			mutate: func(input *CreateInput) {
				input.ExpiresAt = validExpirySeconds
			},
			wantExpires: createTimePtr(t, validExpirySeconds),
			wantStatus:  "active",
		},
		{
			name: "milliseconds expiry",
			mutate: func(input *CreateInput) {
				input.ExpiresAt = validExpiryMillis
			},
			wantExpires: createTimePtr(t, validExpiryMillis),
			wantStatus:  "active",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeyCreateStoreStub{}
			service := newCreateTestService(store, createTestOptions{})
			input := validCreateInput()
			test.mutate(&input)
			_, err := service.Create(context.Background(), input)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Create() error = %v, want containing %q", err, test.wantError)
				}
				if store.createCalls != 0 {
					t.Fatalf("create calls = %d, want 0", store.createCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if !sameOptionalTime(store.createInput.ExpiresAt, test.wantExpires) {
				t.Fatalf("expiresAt = %v, want %v", store.createInput.ExpiresAt, test.wantExpires)
			}
			if store.createInput.Description != nil &&
				strings.TrimSpace(*store.createInput.Description) == "" {
				t.Fatalf("empty description was stored: %#v", store.createInput.Description)
			}
			if store.createInput.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", store.createInput.Status, test.wantStatus)
			}
		})
	}
}

func TestServiceCreateNormalizesQuotaLimitsWithJSONNumberPrecision(t *testing.T) {
	store := &managementAPIKeyCreateStoreStub{}
	service := newCreateTestService(store, createTestOptions{})
	input := validCreateInput()
	input.QuotaLimits = map[string]any{
		"hourly": map[string]any{
			"enabled": true,
			"hours":   json.Number("720"),
			"limit":   json.Number("1.000001"),
		},
		"daily": map[string]any{
			"enabled": true,
			"limit":   json.Number("9007199254740991"),
		},
		"weekly":  map[string]any{"enabled": true, "limit": json.Number("9.007199254740991e15")},
		"monthly": map[string]any{"enabled": true, "limit": json.Number("3.5")},
		"total":   map[string]any{"enabled": true, "limit": json.Number("4e2")},
	}

	result, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.createInput.QuotaLimitsJSON == nil {
		t.Fatal("quota limits JSON = nil")
	}
	var stored map[string]map[string]any
	decoder := json.NewDecoder(strings.NewReader(*store.createInput.QuotaLimitsJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&stored); err != nil {
		t.Fatalf("decode quota JSON: %v", err)
	}
	if stored["hourly"]["limit"] != json.Number("1.000001") ||
		stored["daily"]["limit"] != json.Number("9007199254740991") ||
		stored["weekly"]["limit"] != json.Number("9.007199254740991e15") ||
		stored["total"]["limit"] != json.Number("4e2") ||
		store.createInput.HourlyQuotaHours == nil ||
		*store.createInput.HourlyQuotaHours != 720 {
		t.Fatalf("stored quota = %#v input=%+v", stored, store.createInput)
	}
	if result.QuotaLimits.Hourly == nil ||
		result.QuotaLimits.Hourly.Hours != 720 ||
		result.QuotaLimits.Hourly.Limit != 1.000001 ||
		result.QuotaLimits.Daily == nil ||
		result.QuotaLimits.Daily.Limit != 9007199254740991 {
		t.Fatalf("result quota = %+v", result.QuotaLimits)
	}

	for _, empty := range []any{nil, map[string]any{}} {
		store := &managementAPIKeyCreateStoreStub{}
		service := newCreateTestService(store, createTestOptions{})
		input := validCreateInput()
		input.QuotaLimits = empty
		if _, err := service.Create(context.Background(), input); err != nil {
			t.Fatalf("Create(empty quota %#v) error = %v", empty, err)
		}
		if store.createInput.QuotaLimitsJSON != nil ||
			store.createInput.HourlyQuotaHours != nil {
			t.Fatalf("empty quota stored = %+v", store.createInput)
		}
	}
}

func TestServiceCreateRejectsInvalidQuotaLimits(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "not object", value: []any{}},
		{name: "unknown root field", value: map[string]any{"yearly": map[string]any{}}},
		{name: "item not object", value: map[string]any{"daily": 1}},
		{name: "unknown item field", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": 1, "hours": 1}}},
		{name: "enabled false", value: map[string]any{"daily": map[string]any{"enabled": false, "limit": 1}}},
		{name: "enabled missing", value: map[string]any{"daily": map[string]any{"limit": 1}}},
		{name: "zero", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": 0}}},
		{name: "negative", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": -1}}},
		{name: "above max safe integer", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": json.Number("9007199254740992")}}},
		{name: "fraction above max safe integer", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": json.Number("9007199254740991.1")}}},
		{name: "scientific fraction above max safe integer", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": json.Number("9.0071992547409911e15")}}},
		{name: "scientific integer above max safe integer", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": json.Number("9.007199254740992e15")}}},
		{name: "seven decimals", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": json.Number("1.0000001")}}},
		{name: "nan", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": json.Number("NaN")}}},
		{name: "infinity", value: map[string]any{"daily": map[string]any{"enabled": true, "limit": json.Number("Infinity")}}},
		{name: "hourly hours missing", value: map[string]any{"hourly": map[string]any{"enabled": true, "limit": 1}}},
		{name: "hourly hours fractional", value: map[string]any{"hourly": map[string]any{"enabled": true, "limit": 1, "hours": json.Number("1.5")}}},
		{name: "hourly hours zero", value: map[string]any{"hourly": map[string]any{"enabled": true, "limit": 1, "hours": 0}}},
		{name: "hourly hours above max", value: map[string]any{"hourly": map[string]any{"enabled": true, "limit": 1, "hours": 721}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeyCreateStoreStub{}
			service := newCreateTestService(store, createTestOptions{})
			input := validCreateInput()
			input.QuotaLimits = test.value
			_, err := service.Create(context.Background(), input)
			if err == nil {
				t.Fatal("Create() error = nil")
			}
			if store.createCalls != 0 {
				t.Fatalf("create calls = %d, want 0", store.createCalls)
			}
		})
	}
}

func TestServiceCreateScheduleUsesSettingsTimezoneOverridesStatusAndStoresNextCheck(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 30, 0, 0, time.UTC)
	schedule := map[string]any{
		"enabled": true,
		"mode":    "allow_windows",
		"windows": []any{map[string]any{
			"daysOfWeek": []any{json.Number("1")},
			"start":      "08:00",
			"end":        "10:00",
		}},
	}
	tests := []struct {
		name         string
		timezone     string
		timezoneOK   bool
		timezoneErr  error
		wantTimezone string
		wantStatus   string
		wantNext     time.Time
	}{
		{
			name:         "configured timezone allows current request",
			timezone:     " Asia/Shanghai ",
			timezoneOK:   true,
			wantTimezone: "Asia/Shanghai",
			wantStatus:   "active",
			wantNext:     time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC),
		},
		{
			name:         "missing timezone falls back to UTC",
			wantTimezone: "UTC",
			wantStatus:   "disabled",
			wantNext:     time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC),
		},
		{
			name:         "empty timezone falls back to UTC",
			timezone:     " ",
			timezoneOK:   true,
			wantTimezone: "UTC",
			wantStatus:   "disabled",
			wantNext:     time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC),
		},
		{
			name:         "timezone read failure falls back to UTC",
			timezoneErr:  errors.New("settings unavailable"),
			wantTimezone: "UTC",
			wantStatus:   "disabled",
			wantNext:     time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeyCreateStoreStub{
				timezone:    test.timezone,
				timezoneOK:  test.timezoneOK,
				timezoneErr: test.timezoneErr,
			}
			service := newCreateTestService(store, createTestOptions{now: now})
			input := validCreateInput()
			input.Status = "disabled"
			input.AvailabilitySchedule = schedule
			result, err := service.Create(context.Background(), input)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if store.timezoneCalls != 1 {
				t.Fatalf("timezone calls = %d, want 1", store.timezoneCalls)
			}
			if store.createInput.Status != test.wantStatus ||
				store.createInput.AvailabilityScheduleJSON == nil ||
				store.createInput.AvailabilityScheduleNextCheckAt == nil ||
				!store.createInput.AvailabilityScheduleNextCheckAt.Equal(test.wantNext) {
				t.Fatalf("create input = %+v", store.createInput)
			}
			if result.Status != test.wantStatus ||
				result.AvailabilitySchedule["timezone"] != test.wantTimezone {
				t.Fatalf("result = %+v", result)
			}
			var stored map[string]any
			if err := json.Unmarshal([]byte(*store.createInput.AvailabilityScheduleJSON), &stored); err != nil {
				t.Fatalf("decode schedule: %v", err)
			}
			if stored["timezone"] != test.wantTimezone {
				t.Fatalf("stored timezone = %#v", stored["timezone"])
			}
		})
	}
}

func TestServiceCreateMapsRouteAndDuplicateErrors(t *testing.T) {
	tests := []struct {
		name      string
		storeErr  error
		wantError string
	}{
		{
			name:      "route missing",
			storeErr:  port.ErrManagementAPIKeyRouteStrategyNotFound,
			wantError: "API Key 绑定的策略路由不存在或不属于当前用户",
		},
		{
			name:      "route disabled",
			storeErr:  port.ErrManagementAPIKeyRouteStrategyDisabled,
			wantError: "API Key 只能绑定启用状态的策略路由",
		},
		{
			name:      "duplicate name",
			storeErr:  port.ErrManagementAPIKeyNameExists,
			wantError: "API Key 名称已存在：生产 Key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeyCreateStoreStub{createErrs: []error{test.storeErr}}
			service := newCreateTestService(store, createTestOptions{})
			input := validCreateInput()
			input.Name = " 生产 Key "
			_, err := service.Create(context.Background(), input)
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("Create() error = %v, want %q", err, test.wantError)
			}
			if store.createCalls != 1 {
				t.Fatalf("create calls = %d, want 1", store.createCalls)
			}
		})
	}
}

func TestServiceCreateClassifiesValidationErrorsWithoutMatchingMessages(t *testing.T) {
	tests := []CreateInput{
		{},
		func() CreateInput {
			input := validCreateInput()
			input.Description = 1
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Status = "invalid"
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.QuotaLimits = []any{}
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.AvailabilitySchedule = []any{}
			return input
		}(),
	}
	for index, input := range tests {
		service := newCreateTestService(&managementAPIKeyCreateStoreStub{}, createTestOptions{})
		_, err := service.Create(context.Background(), input)
		if err == nil {
			t.Fatalf("case %d Create() error = nil", index)
		}
		if !IsAPIKeyCreateValidationError(err) {
			t.Fatalf("case %d Create() error = %T %v, want typed validation error", index, err, err)
		}
	}
	if IsAPIKeyCreateValidationError(errors.New("postgres unavailable")) {
		t.Fatal("internal error was classified as validation")
	}
}

func TestServiceCreateRetriesOnlyDuplicateHashUpToThreeAttempts(t *testing.T) {
	t.Run("second secret succeeds", func(t *testing.T) {
		store := &managementAPIKeyCreateStoreStub{
			createErrs: []error{port.ErrManagementAPIKeyHashExists, nil},
		}
		service := NewServiceWithOptions(ServiceOptions{
			ListReader:               store,
			Creator:                  store,
			UsageStatsTimezoneReader: store,
			Invalidator:              &managementAPIKeyInvalidatorStub{},
			Secret:                   "management-api-key-create-test",
			Now:                      func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) },
			NewID:                    sequentialCreateIDs("key_first", "key_second"),
			NewSecret:                sequentialCreateSecrets("sk-first-secret-0123456789", "sk-second-secret-0123456789"),
		})
		result, err := service.Create(context.Background(), validCreateInput())
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if store.createCalls != 2 ||
			len(store.createInputs) != 2 ||
			store.createInputs[0].ID != "key_first" ||
			store.createInputs[1].ID != "key_second" ||
			store.createInputs[0].KeyHash == store.createInputs[1].KeyHash ||
			result.Key != "sk-second-secret-0123456789" ||
			result.ID != "key_second" {
			t.Fatalf("inputs=%+v result=%+v", store.createInputs, result)
		}
	})

	t.Run("three duplicate hashes fail", func(t *testing.T) {
		store := &managementAPIKeyCreateStoreStub{
			createErrs: []error{
				port.ErrManagementAPIKeyHashExists,
				port.ErrManagementAPIKeyHashExists,
				port.ErrManagementAPIKeyHashExists,
			},
		}
		service := NewServiceWithOptions(ServiceOptions{
			ListReader:               store,
			Creator:                  store,
			UsageStatsTimezoneReader: store,
			Secret:                   "management-api-key-create-test",
			NewID:                    sequentialCreateIDs("key_1", "key_2", "key_3"),
			NewSecret:                sequentialCreateSecrets("sk-one-0123456789", "sk-two-0123456789", "sk-three-0123456789"),
		})
		_, err := service.Create(context.Background(), validCreateInput())
		if !errors.Is(err, port.ErrManagementAPIKeyHashExists) {
			t.Fatalf("Create() error = %v, want duplicate hash", err)
		}
		if store.createCalls != 3 {
			t.Fatalf("create calls = %d, want 3", store.createCalls)
		}
	})

	t.Run("name conflict is not retried", func(t *testing.T) {
		store := &managementAPIKeyCreateStoreStub{
			createErrs: []error{port.ErrManagementAPIKeyNameExists},
		}
		service := newCreateTestService(store, createTestOptions{})
		_, _ = service.Create(context.Background(), validCreateInput())
		if store.createCalls != 1 {
			t.Fatalf("create calls = %d, want 1", store.createCalls)
		}
	})
}

func TestServiceCreateInvalidationsAreBestEffortAndSkipValidation(t *testing.T) {
	store := &managementAPIKeyCreateStoreStub{}
	invalidator := &managementAPIKeyInvalidatorStub{
		runtimeErr: errors.New("runtime unavailable"),
		quotaErr:   errors.New("quota unavailable"),
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:               store,
		Creator:                  store,
		UsageStatsTimezoneReader: store,
		Invalidator:              invalidator,
		Secret:                   "management-api-key-create-test",
		NewID:                    func(string) string { return "key_created" },
		NewSecret:                func() (string, error) { return "sk-created-secret-0123456789", nil },
	})

	result, err := service.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.ID != "key_created" ||
		invalidator.calls != 2 ||
		invalidator.runtimeReason != "api_key_created" ||
		invalidator.quotaReason != "api_key_created" ||
		invalidator.quotaAPIKeyID != "key_created" {
		t.Fatalf("result=%+v invalidator=%+v", result, invalidator)
	}
	if invalidator.validationContextHasDeadline || invalidator.validationContextErr != nil {
		t.Fatalf("validation invalidation was called: %+v", invalidator)
	}
}

func TestServiceCreateInvalidationsUseDetachedBoundedContextAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &managementAPIKeyCreateStoreStub{afterCreate: cancel}
	invalidator := &managementAPIKeyCreateInvalidatorContextStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:               store,
		Creator:                  store,
		UsageStatsTimezoneReader: store,
		Invalidator:              invalidator,
		Secret:                   "management-api-key-create-test",
		NewID:                    func(string) string { return "key_created" },
		NewSecret:                func() (string, error) { return "sk-created-secret-0123456789", nil },
	})

	result, err := service.Create(ctx, validCreateInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.ID != "key_created" || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("result=%+v request context error=%v", result, ctx.Err())
	}
	if invalidator.runtimeCalls != 1 ||
		invalidator.quotaCalls != 1 ||
		invalidator.validationCalls != 0 {
		t.Fatalf("invalidator calls = %+v", invalidator)
	}
	if invalidator.runtimeContextErr != nil ||
		invalidator.quotaContextErr != nil ||
		!invalidator.runtimeContextHasDeadline ||
		!invalidator.quotaContextHasDeadline {
		t.Fatalf("invalidation contexts = %+v", invalidator)
	}
	if !invalidator.runtimeDeadline.Equal(invalidator.quotaDeadline) {
		t.Fatalf(
			"runtime deadline=%s quota deadline=%s, want one shared timeout context",
			invalidator.runtimeDeadline,
			invalidator.quotaDeadline,
		)
	}
}

type createTestOptions struct {
	now    time.Time
	secret string
	id     string
}

func newCreateTestService(store *managementAPIKeyCreateStoreStub, opts createTestOptions) *Service {
	now := opts.now
	if now.IsZero() {
		now = time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	}
	secret := opts.secret
	if secret == "" {
		secret = "sk-default-secret-0123456789"
	}
	id := opts.id
	if id == "" {
		id = "key_created"
	}
	return NewServiceWithOptions(ServiceOptions{
		ListReader:               store,
		Creator:                  store,
		UsageStatsTimezoneReader: store,
		Invalidator:              &managementAPIKeyInvalidatorStub{},
		Secret:                   "management-api-key-create-test",
		Now:                      func() time.Time { return now },
		NewID:                    func(string) string { return id },
		NewSecret:                func() (string, error) { return secret, nil },
	})
}

func validCreateInput() CreateInput {
	return CreateInput{
		ActorSystemAccountID: "sys_actor",
		ActorRole:            "user",
		Name:                 "生产 Key",
		RouteStrategyID:      "route_1",
	}
}

func createTimePtr(t *testing.T, raw string) *time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse time fixture: %v", err)
	}
	value = value.UTC()
	return &value
}

func sameOptionalTime(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sequentialCreateIDs(values ...string) func(string) string {
	index := 0
	return func(string) string {
		value := values[index]
		index++
		return value
	}
}

func sequentialCreateSecrets(values ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		value := values[index]
		index++
		return value, nil
	}
}

type managementAPIKeyCreateStoreStub struct {
	createInput   port.ManagementAPIKeyCreateInput
	createInputs  []port.ManagementAPIKeyCreateInput
	createErrs    []error
	timezone      string
	timezoneOK    bool
	timezoneErr   error
	createCalls   int
	timezoneCalls int
	afterCreate   func()
}

func (s *managementAPIKeyCreateStoreStub) ListManagementAPIKeys(
	context.Context,
	port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	return port.ManagementAPIKeyListPage{}, nil
}

func (s *managementAPIKeyCreateStoreStub) ListManagementAPIKeyUsageTotals(
	context.Context,
	[]port.ManagementAPIKeyUsageScope,
) ([]port.ManagementAPIKeyUsageRow, error) {
	return nil, nil
}

func (s *managementAPIKeyCreateStoreStub) CreateManagementAPIKey(
	_ context.Context,
	input port.ManagementAPIKeyCreateInput,
) (port.ManagementAPIKeyListRow, error) {
	s.createCalls++
	s.createInput = input
	s.createInputs = append(s.createInputs, input)
	var err error
	if s.createCalls <= len(s.createErrs) {
		err = s.createErrs[s.createCalls-1]
	}
	if err != nil {
		return port.ManagementAPIKeyListRow{}, err
	}
	if s.afterCreate != nil {
		s.afterCreate()
	}
	return port.ManagementAPIKeyListRow{
		ID:                       input.ID,
		SystemAccountID:          input.SystemAccountID,
		SystemAccountName:        "Owner " + input.SystemAccountID,
		Name:                     input.Name,
		Description:              input.Description,
		KeyPrefix:                input.KeyPrefix,
		KeySuffix:                input.KeySuffix,
		Status:                   input.Status,
		IsDefault:                input.IsDefault,
		RouteStrategyID:          input.RouteStrategyID,
		RouteStrategyName:        "默认策略",
		RouteStrategyMode:        "normal",
		RouteStrategyStatus:      "active",
		ExpiresAt:                input.ExpiresAt,
		QuotaLimitsJSON:          input.QuotaLimitsJSON,
		AvailabilityScheduleJSON: input.AvailabilityScheduleJSON,
	}, nil
}

func (s *managementAPIKeyCreateStoreStub) GetManagementUsageStatsTimezone(
	context.Context,
) (string, bool, error) {
	s.timezoneCalls++
	return s.timezone, s.timezoneOK, s.timezoneErr
}

var _ port.ManagementAPIKeyListReader = (*managementAPIKeyCreateStoreStub)(nil)
var _ port.ManagementAPIKeyCreator = (*managementAPIKeyCreateStoreStub)(nil)
var _ port.ManagementUsageStatsTimezoneReader = (*managementAPIKeyCreateStoreStub)(nil)

type managementAPIKeyCreateInvalidatorContextStub struct {
	runtimeCalls              int
	quotaCalls                int
	validationCalls           int
	runtimeContextErr         error
	quotaContextErr           error
	runtimeContextHasDeadline bool
	quotaContextHasDeadline   bool
	runtimeDeadline           time.Time
	quotaDeadline             time.Time
}

func (s *managementAPIKeyCreateInvalidatorContextStub) InvalidateAPIKeyValidationCache(
	context.Context,
) error {
	s.validationCalls++
	return nil
}

func (s *managementAPIKeyCreateInvalidatorContextStub) InvalidateGatewayRuntime(
	ctx context.Context,
	_ string,
) error {
	s.runtimeCalls++
	s.runtimeContextErr = ctx.Err()
	s.runtimeDeadline, s.runtimeContextHasDeadline = ctx.Deadline()
	return nil
}

func (s *managementAPIKeyCreateInvalidatorContextStub) InvalidateAPIKeyQuotaChanged(
	ctx context.Context,
	_ string,
	_ string,
) error {
	s.quotaCalls++
	s.quotaContextErr = ctx.Err()
	s.quotaDeadline, s.quotaContextHasDeadline = ctx.Deadline()
	return nil
}

var _ APIKeyGatewayCacheInvalidator = (*managementAPIKeyCreateInvalidatorContextStub)(nil)
