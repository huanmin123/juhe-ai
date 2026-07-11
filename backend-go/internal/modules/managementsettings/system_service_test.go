package managementsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/systemsettings"
)

func TestSystemServiceGetReturnsValidatedIndependentSnapshot(t *testing.T) {
	stored := validSystemSettingsSnapshot(t)
	store := &systemSettingsStoreStub{settings: stored}
	service := NewSystemService(store)

	settings, err := service.Get(context.Background())

	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if store.readCalls != 1 {
		t.Fatalf("ManagementSystemSettings() calls = %d, want 1", store.readCalls)
	}
	value, ok := settings.Value("usageHotWindowRefreshIntervalSeconds")
	if !ok || string(value) != "60" {
		t.Fatalf("usageHotWindowRefreshIntervalSeconds = %q, %v; want 60", value, ok)
	}
	priority, ok := settings.Value("gptPriorityPriceMultiplier")
	if !ok || string(priority) != "2" {
		t.Fatalf("gptPriorityPriceMultiplier = %q, %v; want 2", priority, ok)
	}
	flex, ok := settings.Value("gptFlexPriceMultiplier")
	if !ok || string(flex) != "0.5" {
		t.Fatalf("gptFlexPriceMultiplier = %q, %v; want 0.5", flex, ok)
	}
	returnedValues := settings.Values()
	returnedValues["gatewayTextRawBodyLimitMegabytes"][0] = '9'
	storedValue, _ := stored.Value("gatewayTextRawBodyLimitMegabytes")
	if string(storedValue) != "1" {
		t.Fatalf("Get() exposed store snapshot memory: %s", storedValue)
	}
}

func TestSystemServiceGetRejectsInvalidStoreSnapshot(t *testing.T) {
	store := &systemSettingsStoreStub{}
	service := NewSystemService(store)

	_, err := service.Get(context.Background())

	if err == nil {
		t.Fatal("Get() error = nil, want invalid full snapshot error")
	}
}

func TestSystemServiceUpdateValidatesRawPatchUsesUTCAndInvalidatesSeparately(t *testing.T) {
	before := validSystemSettingsSnapshot(t)
	patch, err := systemsettings.NewPatch(map[string]json.RawMessage{
		"gatewayTextRawBodyLimitMegabytes":     json.RawMessage(`32`),
		"usageHotWindowRefreshIntervalSeconds": json.RawMessage(`600`),
		"gptPriorityPriceMultiplier":           json.RawMessage(`2.5`),
	})
	if err != nil {
		t.Fatalf("NewPatch() error = %v", err)
	}
	after, err := before.Apply(patch)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	now := time.Date(2026, 7, 10, 18, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	store := &systemSettingsStoreStub{
		updateResult: port.ManagementSystemSettingsUpdateResult{
			Before:   before,
			Settings: after,
		},
	}
	invalidator := &systemSettingsInvalidatorStub{}
	service := NewSystemServiceWithOptions(SystemServiceOptions{
		Store:       store,
		Invalidator: invalidator,
		Now:         func() time.Time { return now },
	})

	result, err := service.Update(context.Background(), SystemUpdateInput{
		Values: map[string]json.RawMessage{
			"usageHotWindowRefreshIntervalSeconds": json.RawMessage(` 600 `),
			"gatewayTextRawBodyLimitMegabytes":     json.RawMessage(`32`),
			"gptPriorityPriceMultiplier":           json.RawMessage(`2.5`),
		},
	})

	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.updateCalls != 1 {
		t.Fatalf("UpdateManagementSystemSettings() calls = %d, want 1", store.updateCalls)
	}
	if !store.updateInput.UpdatedAt.Equal(now.UTC()) || store.updateInput.UpdatedAt.Location() != time.UTC {
		t.Fatalf("UpdatedAt = %v, want %v", store.updateInput.UpdatedAt, now.UTC())
	}
	entries := store.updateInput.Patch.Entries()
	if len(entries) != 3 ||
		entries[0].Key != "gatewayTextRawBodyLimitMegabytes" ||
		string(entries[0].Value) != "32" ||
		entries[1].Key != "gptPriorityPriceMultiplier" ||
		string(entries[1].Value) != "2.5" ||
		entries[2].Key != "usageHotWindowRefreshIntervalSeconds" ||
		string(entries[2].Value) != "600" {
		t.Fatalf("store patch entries = %+v", entries)
	}
	resultValue, _ := result.Settings.Value("gatewayTextRawBodyLimitMegabytes")
	if string(resultValue) != "32" {
		t.Fatalf("updated value = %s, want 32", resultValue)
	}
	decimalValue, _ := result.Settings.Value("gptPriorityPriceMultiplier")
	if string(decimalValue) != "2.5" {
		t.Fatalf("updated decimal value = %s, want 2.5", decimalValue)
	}
	if invalidator.systemCacheCalls != 1 {
		t.Fatalf("InvalidateSystemSettingsCache() calls = %d, want 1", invalidator.systemCacheCalls)
	}
	if invalidator.runtimeCalls != 1 || invalidator.runtimeReasons[0] != SystemSettingsUpdatedReason {
		t.Fatalf("InvalidateGatewayRuntime() calls=%d reasons=%v", invalidator.runtimeCalls, invalidator.runtimeReasons)
	}
	if len(invalidator.callOrder) != 2 ||
		invalidator.callOrder[0] != "system_cache" ||
		invalidator.callOrder[1] != "gateway_runtime" {
		t.Fatalf("invalidation order = %v", invalidator.callOrder)
	}
}

func TestSystemServiceUpdateRejectsUsageStatsTimezoneBeforeStore(t *testing.T) {
	store := &systemSettingsStoreStub{}
	invalidator := &systemSettingsInvalidatorStub{}
	service := NewSystemServiceWithOptions(SystemServiceOptions{
		Store:       store,
		Invalidator: invalidator,
	})

	_, err := service.Update(context.Background(), SystemUpdateInput{
		Values: map[string]json.RawMessage{
			systemsettings.UsageStatsTimezoneKey: json.RawMessage(`"UTC"`),
		},
	})

	if !errors.Is(err, ErrUsageStatsTimezoneOnlineUpdateUnsupported) {
		t.Fatalf("Update() error = %v, want %v", err, ErrUsageStatsTimezoneOnlineUpdateUnsupported)
	}
	if err.Error() != "PostgreSQL 模式下暂不支持在线修改统计时区，请停机后通过离线迁移 / 重建流程调整" {
		t.Fatalf("timezone error = %q", err)
	}
	if store.updateCalls != 0 {
		t.Fatalf("store update calls = %d, want 0", store.updateCalls)
	}
	if invalidator.systemCacheCalls != 0 || invalidator.runtimeCalls != 0 {
		t.Fatalf("invalidation calls = %d/%d, want 0/0", invalidator.systemCacheCalls, invalidator.runtimeCalls)
	}
}

func TestSystemServiceUpdateRejectsEmptyUnknownNullAndNonIntegerInput(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]json.RawMessage
		want   error
	}{
		{name: "nil", values: nil, want: ErrSystemUpdateEmpty},
		{name: "empty", values: map[string]json.RawMessage{}, want: ErrSystemUpdateEmpty},
		{name: "unknown", values: map[string]json.RawMessage{"unknown": json.RawMessage(`1`)}},
		{name: "null", values: map[string]json.RawMessage{"accountTestTaskConcurrency": json.RawMessage(`null`)}},
		{name: "float", values: map[string]json.RawMessage{"accountTestTaskConcurrency": json.RawMessage(`1.0`)}},
		{name: "numeric string", values: map[string]json.RawMessage{"accountTestTaskConcurrency": json.RawMessage(`"1"`)}},
		{name: "trailing json", values: map[string]json.RawMessage{"accountTestTaskConcurrency": json.RawMessage(`1 true`)}},
		{name: "decimal string", values: map[string]json.RawMessage{"gptPriorityPriceMultiplier": json.RawMessage(`"2"`)}},
		{name: "decimal null", values: map[string]json.RawMessage{"gptFlexPriceMultiplier": json.RawMessage(`null`)}},
		{name: "decimal out of range", values: map[string]json.RawMessage{"gptFlexPriceMultiplier": json.RawMessage(`100.0001`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemSettingsStoreStub{}
			service := NewSystemService(store)

			_, err := service.Update(context.Background(), SystemUpdateInput{Values: tt.values})

			if err == nil {
				t.Fatal("Update() error = nil, want validation error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Update() error = %v, want %v", err, tt.want)
			}
			if store.updateCalls != 0 {
				t.Fatalf("store update calls = %d, want 0", store.updateCalls)
			}
		})
	}
}

func TestSystemServiceUpdateStoreErrorSkipsInvalidation(t *testing.T) {
	wantErr := errors.New("postgres down")
	store := &systemSettingsStoreStub{updateErr: wantErr}
	invalidator := &systemSettingsInvalidatorStub{}
	service := NewSystemServiceWithOptions(SystemServiceOptions{
		Store:       store,
		Invalidator: invalidator,
	})

	_, err := service.Update(context.Background(), SystemUpdateInput{
		Values: map[string]json.RawMessage{
			"accountTestTaskConcurrency": json.RawMessage(`10`),
			"gptFlexPriceMultiplier":     json.RawMessage(`0.75`),
		},
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("Update() error = %v, want %v", err, wantErr)
	}
	if store.updateCalls != 1 {
		t.Fatalf("store update calls = %d, want 1", store.updateCalls)
	}
	if invalidator.systemCacheCalls != 0 || invalidator.runtimeCalls != 0 {
		t.Fatalf("invalidation calls = %d/%d, want 0/0", invalidator.systemCacheCalls, invalidator.runtimeCalls)
	}
}

func TestSystemServiceUpdateRejectsInvalidReturnedSnapshotBeforeInvalidation(t *testing.T) {
	valid := validSystemSettingsSnapshot(t)
	tests := []struct {
		name   string
		result port.ManagementSystemSettingsUpdateResult
	}{
		{
			name: "invalid before",
			result: port.ManagementSystemSettingsUpdateResult{
				Settings: valid,
			},
		},
		{
			name: "invalid settings",
			result: port.ManagementSystemSettingsUpdateResult{
				Before: valid,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemSettingsStoreStub{updateResult: tt.result}
			invalidator := &systemSettingsInvalidatorStub{}
			service := NewSystemServiceWithOptions(SystemServiceOptions{
				Store:       store,
				Invalidator: invalidator,
			})

			_, err := service.Update(context.Background(), SystemUpdateInput{
				Values: map[string]json.RawMessage{
					"accountTestTaskConcurrency": json.RawMessage(`10`),
					"gptFlexPriceMultiplier":     json.RawMessage(`0.75`),
				},
			})

			if err == nil {
				t.Fatal("Update() error = nil, want invalid result snapshot error")
			}
			if invalidator.systemCacheCalls != 0 || invalidator.runtimeCalls != 0 {
				t.Fatalf("invalidation calls = %d/%d, want 0/0", invalidator.systemCacheCalls, invalidator.runtimeCalls)
			}
		})
	}
}

func TestSystemServiceUpdateIgnoresEachInvalidationErrorAndStillCallsBoth(t *testing.T) {
	settings := validSystemSettingsSnapshot(t)
	store := &systemSettingsStoreStub{
		updateResult: port.ManagementSystemSettingsUpdateResult{
			Before:   settings,
			Settings: settings,
		},
	}
	invalidator := &systemSettingsInvalidatorStub{
		systemCacheErr: errors.New("cache redis down"),
		runtimeErr:     errors.New("state redis down"),
	}
	var logs bytes.Buffer
	service := NewSystemServiceWithOptions(SystemServiceOptions{
		Store:       store,
		Invalidator: invalidator,
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.Update(requestCtx, SystemUpdateInput{
		Values: map[string]json.RawMessage{
			"accountTestTaskConcurrency": json.RawMessage(`1`),
		},
	})

	if err != nil {
		t.Fatalf("Update() error = %v, want nil despite invalidation errors", err)
	}
	if result.Settings.Len() != 55 {
		t.Fatalf("result settings length = %d, want 55", result.Settings.Len())
	}
	if invalidator.systemCacheCalls != 1 || invalidator.runtimeCalls != 1 {
		t.Fatalf("invalidation calls = %d/%d, want 1/1", invalidator.systemCacheCalls, invalidator.runtimeCalls)
	}
	if invalidator.systemCacheContextErr != nil || invalidator.runtimeContextErr != nil {
		t.Fatalf(
			"invalidation context errors = %v/%v, want detached contexts",
			invalidator.systemCacheContextErr,
			invalidator.runtimeContextErr,
		)
	}
	for _, event := range []string{
		"system_settings_cache_invalidation_failed",
		"system_settings_gateway_runtime_invalidation_failed",
	} {
		if !strings.Contains(logs.String(), event) {
			t.Fatalf("logs = %s, want event %q", logs.String(), event)
		}
	}
}

func TestSystemServiceRequiresStore(t *testing.T) {
	service := NewSystemService(nil)
	if _, err := service.Get(context.Background()); err == nil {
		t.Fatal("Get() error = nil, want required store error")
	}
	if _, err := service.Update(context.Background(), SystemUpdateInput{
		Values: map[string]json.RawMessage{
			"accountTestTaskConcurrency": json.RawMessage(`1`),
		},
	}); err == nil {
		t.Fatal("Update() error = nil, want required store error")
	}
}

func validSystemSettingsSnapshot(t *testing.T) systemsettings.Snapshot {
	t.Helper()
	values := make(map[string]json.RawMessage, len(systemsettings.Definitions()))
	for _, definition := range systemsettings.Definitions() {
		if definition.Kind == systemsettings.ValueKindTimezone {
			values[definition.Key] = json.RawMessage(`"UTC"`)
			continue
		}
		if definition.Kind == systemsettings.ValueKindDecimal {
			switch definition.Key {
			case "gptPriorityPriceMultiplier":
				values[definition.Key] = json.RawMessage(`2`)
			case "gptFlexPriceMultiplier":
				values[definition.Key] = json.RawMessage(`0.5`)
			default:
				t.Fatalf("unexpected decimal system setting %q", definition.Key)
			}
			continue
		}
		values[definition.Key] = json.RawMessage(strconv.Itoa(definition.Minimum))
	}
	settings, err := systemsettings.NewSnapshot(values)
	if err != nil {
		t.Fatalf("NewSnapshot(valid fixture) error = %v", err)
	}
	return settings
}

type systemSettingsStoreStub struct {
	settings     systemsettings.Snapshot
	readErr      error
	readCalls    int
	updateInput  port.ManagementSystemSettingsUpdateInput
	updateResult port.ManagementSystemSettingsUpdateResult
	updateErr    error
	updateCalls  int
}

func (s *systemSettingsStoreStub) ManagementSystemSettings(context.Context) (systemsettings.Snapshot, error) {
	s.readCalls++
	return s.settings, s.readErr
}

func (s *systemSettingsStoreStub) UpdateManagementSystemSettings(
	_ context.Context,
	input port.ManagementSystemSettingsUpdateInput,
) (port.ManagementSystemSettingsUpdateResult, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updateResult, s.updateErr
}

var _ SystemStore = (*systemSettingsStoreStub)(nil)

type systemSettingsInvalidatorStub struct {
	systemCacheCalls      int
	systemCacheErr        error
	systemCacheContextErr error
	runtimeCalls          int
	runtimeReasons        []string
	runtimeErr            error
	runtimeContextErr     error
	callOrder             []string
}

func (s *systemSettingsInvalidatorStub) InvalidateSystemSettingsCache(ctx context.Context) error {
	s.systemCacheCalls++
	s.systemCacheContextErr = ctx.Err()
	s.callOrder = append(s.callOrder, "system_cache")
	return s.systemCacheErr
}

func (s *systemSettingsInvalidatorStub) InvalidateGatewayRuntime(ctx context.Context, reason string) error {
	s.runtimeCalls++
	s.runtimeContextErr = ctx.Err()
	s.runtimeReasons = append(s.runtimeReasons, reason)
	s.callOrder = append(s.callOrder, "gateway_runtime")
	return s.runtimeErr
}

var _ SystemSettingsInvalidator = (*systemSettingsInvalidatorStub)(nil)
