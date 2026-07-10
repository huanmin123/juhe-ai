package managementsettings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestGetMapsPublicGlobalSettings(t *testing.T) {
	store := &globalSettingsStoreStub{
		publicSettings: port.PublicGlobalSettings{
			AppName: "聚合 AI",
			AppIcon: "/__aisys__/brand-icon.svg",
		},
	}
	service := NewService(store)

	settings, err := service.Get(context.Background())

	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if settings.AppName != "聚合 AI" || settings.AppIcon != "/__aisys__/brand-icon.svg" {
		t.Fatalf("Get() = %+v", settings)
	}
	if store.readCalls != 1 {
		t.Fatalf("PublicGlobalSettings() calls = %d, want 1", store.readCalls)
	}
}

func TestUpdateTrimsPresentFieldsAndPreservesAbsentFields(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 16, 17, 0, time.FixedZone("CST", 8*60*60))
	appName := "  新名称  "
	store := &globalSettingsStoreStub{
		updateResult: port.ManagementGlobalSettingsUpdateResult{
			Before: port.PublicGlobalSettings{
				AppName: "聚合 AI",
				AppIcon: "/__aisys__/brand-icon.svg",
			},
			Settings: port.PublicGlobalSettings{
				AppName: "新名称",
				AppIcon: "/__aisys__/brand-icon.svg",
			},
		},
	}
	invalidator := &globalSettingsCacheInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                          store,
		GlobalSettingsCacheInvalidator: invalidator,
		Now:                            func() time.Time { return now },
	})

	result, err := service.Update(context.Background(), UpdateInput{AppName: &appName})

	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.updateCalls != 1 {
		t.Fatalf("UpdateGlobalSettings() calls = %d, want 1", store.updateCalls)
	}
	if store.updateInput.AppName == nil || *store.updateInput.AppName != "新名称" {
		t.Fatalf("store appName = %#v, want trimmed value", store.updateInput.AppName)
	}
	if store.updateInput.AppIcon != nil {
		t.Fatalf("store appIcon = %#v, want nil for absent field", store.updateInput.AppIcon)
	}
	if !store.updateInput.UpdatedAt.Equal(now.UTC()) || store.updateInput.UpdatedAt.Location() != time.UTC {
		t.Fatalf("store updatedAt = %v, want %v", store.updateInput.UpdatedAt, now.UTC())
	}
	if result.Before.AppName != "聚合 AI" ||
		result.Before.AppIcon != "/__aisys__/brand-icon.svg" ||
		result.Settings.AppName != "新名称" ||
		result.Settings.AppIcon != "/__aisys__/brand-icon.svg" {
		t.Fatalf("Update() = %+v", result)
	}
	if invalidator.calls != 1 {
		t.Fatalf("InvalidateGlobalSettingsCache() calls = %d, want 1", invalidator.calls)
	}

	encoded, err := json.Marshal(result.Settings)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(encoded) != `{"appName":"新名称","appIcon":"/__aisys__/brand-icon.svg"}` {
		t.Fatalf("settings json = %s", encoded)
	}
}

func TestUpdateTrimsAppIconWithoutAddingAppName(t *testing.T) {
	appIcon := "  /brand.svg  "
	store := &globalSettingsStoreStub{
		updateResult: port.ManagementGlobalSettingsUpdateResult{},
	}
	service := NewService(store)

	_, err := service.Update(context.Background(), UpdateInput{AppIcon: &appIcon})

	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.updateInput.AppName != nil {
		t.Fatalf("store appName = %#v, want nil for absent field", store.updateInput.AppName)
	}
	if store.updateInput.AppIcon == nil || *store.updateInput.AppIcon != "/brand.svg" {
		t.Fatalf("store appIcon = %#v, want trimmed value", store.updateInput.AppIcon)
	}
}

func TestUpdateRejectsEmptyInput(t *testing.T) {
	store := &globalSettingsStoreStub{}
	invalidator := &globalSettingsCacheInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                          store,
		GlobalSettingsCacheInvalidator: invalidator,
	})

	_, err := service.Update(context.Background(), UpdateInput{})

	if !errors.Is(err, ErrUpdateEmpty) {
		t.Fatalf("Update() error = %v, want %v", err, ErrUpdateEmpty)
	}
	if store.updateCalls != 0 {
		t.Fatalf("UpdateGlobalSettings() calls = %d, want 0", store.updateCalls)
	}
	if invalidator.calls != 0 {
		t.Fatalf("InvalidateGlobalSettingsCache() calls = %d, want 0", invalidator.calls)
	}
}

func TestUpdateRejectsPresentBlankFields(t *testing.T) {
	tests := []struct {
		name  string
		input UpdateInput
		want  error
	}{
		{
			name:  "app name",
			input: UpdateInput{AppName: stringPointer(" \t ")},
			want:  ErrAppNameEmpty,
		},
		{
			name:  "app icon",
			input: UpdateInput{AppIcon: stringPointer("\r\n")},
			want:  ErrAppIconEmpty,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &globalSettingsStoreStub{}
			invalidator := &globalSettingsCacheInvalidatorStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:                          store,
				GlobalSettingsCacheInvalidator: invalidator,
			})

			_, err := service.Update(context.Background(), tt.input)

			if !errors.Is(err, tt.want) {
				t.Fatalf("Update() error = %v, want %v", err, tt.want)
			}
			if store.updateCalls != 0 {
				t.Fatalf("UpdateGlobalSettings() calls = %d, want 0", store.updateCalls)
			}
			if invalidator.calls != 0 {
				t.Fatalf("InvalidateGlobalSettingsCache() calls = %d, want 0", invalidator.calls)
			}
		})
	}
}

func TestUpdateStoreErrorSkipsInvalidation(t *testing.T) {
	wantErr := errors.New("postgres down")
	store := &globalSettingsStoreStub{updateErr: wantErr}
	invalidator := &globalSettingsCacheInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                          store,
		GlobalSettingsCacheInvalidator: invalidator,
	})

	_, err := service.Update(context.Background(), UpdateInput{AppName: stringPointer("新名称")})

	if !errors.Is(err, wantErr) {
		t.Fatalf("Update() error = %v, want %v", err, wantErr)
	}
	if store.updateCalls != 1 {
		t.Fatalf("UpdateGlobalSettings() calls = %d, want 1", store.updateCalls)
	}
	if invalidator.calls != 0 {
		t.Fatalf("InvalidateGlobalSettingsCache() calls = %d, want 0", invalidator.calls)
	}
}

func TestUpdateIgnoresCacheInvalidationErrorAfterStoreSuccess(t *testing.T) {
	store := &globalSettingsStoreStub{
		updateResult: port.ManagementGlobalSettingsUpdateResult{
			Settings: port.PublicGlobalSettings{
				AppName: "新名称",
				AppIcon: "/brand.svg",
			},
		},
	}
	invalidator := &globalSettingsCacheInvalidatorStub{err: errors.New("redis down")}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                          store,
		GlobalSettingsCacheInvalidator: invalidator,
	})

	result, err := service.Update(context.Background(), UpdateInput{AppName: stringPointer("新名称")})

	if err != nil {
		t.Fatalf("Update() error = %v, want nil despite invalidation error", err)
	}
	if result.Settings.AppName != "新名称" || result.Settings.AppIcon != "/brand.svg" {
		t.Fatalf("Update() = %+v", result)
	}
	if invalidator.calls != 1 {
		t.Fatalf("InvalidateGlobalSettingsCache() calls = %d, want 1", invalidator.calls)
	}
}

type globalSettingsStoreStub struct {
	publicSettings port.PublicGlobalSettings
	readErr        error
	readCalls      int
	updateInput    port.ManagementGlobalSettingsUpdateInput
	updateResult   port.ManagementGlobalSettingsUpdateResult
	updateErr      error
	updateCalls    int
}

func (s *globalSettingsStoreStub) PublicGlobalSettings(context.Context) (port.PublicGlobalSettings, error) {
	s.readCalls++
	return s.publicSettings, s.readErr
}

func (s *globalSettingsStoreStub) UpdateGlobalSettings(_ context.Context, input port.ManagementGlobalSettingsUpdateInput) (port.ManagementGlobalSettingsUpdateResult, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updateResult, s.updateErr
}

type globalSettingsCacheInvalidatorStub struct {
	calls int
	err   error
}

func (s *globalSettingsCacheInvalidatorStub) InvalidateGlobalSettingsCache(context.Context) error {
	s.calls++
	return s.err
}

func stringPointer(value string) *string {
	return &value
}
