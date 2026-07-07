package publicapikeys

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceAddCreatesHashOnlySecretAndNormalizesLimits(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	store := newPublicAPIKeyServiceStore()
	service := NewService(Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID:      fixedID("key_created"),
		NewSecret:  fixedSecret("sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
	})

	response, err := service.Add(context.Background(), AddInput{
		TargetUsername:  "admin",
		Name:            "公开 Key",
		RouteStrategyID: "rts_active",
		Status:          StatusDisabled,
		QuotaLimits: NewJSONValue(map[string]any{
			"daily":  map[string]any{"enabled": true, "limit": json.Number("100.5")},
			"hourly": map[string]any{"enabled": true, "limit": json.Number("10"), "hours": json.Number("2")},
		}, true),
		AvailabilitySchedule: NewJSONValue(map[string]any{
			"enabled":  true,
			"mode":     "allow_windows",
			"timezone": "UTC",
			"windows": []any{
				map[string]any{"daysOfWeek": []any{json.Number("2")}, "start": "09:00", "end": "18:00"},
			},
		}, true),
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if response.Action != "created" || response.APIKey == nil || response.APIKey.Key == "" {
		t.Fatalf("response = %+v", response)
	}
	if response.APIKey.KeyPrefix != "sk-01234" || response.APIKey.KeySuffix != "89abcdef" {
		t.Fatalf("api key prefix/suffix = %q/%q", response.APIKey.KeyPrefix, response.APIKey.KeySuffix)
	}
	if store.createInput.KeyHash != hashSecret(response.APIKey.Key) {
		t.Fatalf("hash = %q, want sha256 of secret", store.createInput.KeyHash)
	}
	if store.createInput.Status != port.PublicAPIKeyStatusActive {
		t.Fatalf("status = %q, want schedule override active", store.createInput.Status)
	}
	if store.createInput.QuotaLimitsJSON == nil || !strings.Contains(*store.createInput.QuotaLimitsJSON, `"daily"`) {
		t.Fatalf("quota json = %v", store.createInput.QuotaLimitsJSON)
	}
	if store.createInput.AvailabilityScheduleJSON == nil || store.createInput.AvailabilityScheduleNextCheckAt == nil {
		t.Fatalf("schedule json/next = %v/%v", store.createInput.AvailabilityScheduleJSON, store.createInput.AvailabilityScheduleNextCheckAt)
	}
	if response.APIKey.QuotaLimits == nil || response.APIKey.AvailabilitySchedule == nil {
		t.Fatalf("response should expose normalized quota/schedule: %+v", response.APIKey)
	}
}

func TestServiceAddRequiresExistingActiveTargetAndRouteStrategy(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*publicAPIKeyServiceStore)
		wantErr   error
	}{
		{
			name: "missing target",
			configure: func(store *publicAPIKeyServiceStore) {
				delete(store.targetsByUsername, "admin")
			},
			wantErr: ErrTargetNotFound,
		},
		{
			name: "disabled target",
			configure: func(store *publicAPIKeyServiceStore) {
				target := store.targetsByUsername["admin"]
				target.Status = "disabled"
				store.targetsByUsername["admin"] = target
				store.targetsByID[target.ID] = target
			},
			wantErr: ErrTargetDisabled,
		},
		{
			name: "missing route",
			configure: func(store *publicAPIKeyServiceStore) {
				delete(store.routes, "sys_admin/rts_active")
			},
			wantErr: ErrRouteStrategyNotFound,
		},
		{
			name: "disabled route",
			configure: func(store *publicAPIKeyServiceStore) {
				route := store.routes["sys_admin/rts_active"]
				route.Status = port.PublicRouteStrategyStatusDisabled
				store.routes["sys_admin/rts_active"] = route
			},
			wantErr: ErrRouteStrategyDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPublicAPIKeyServiceStore()
			tt.configure(store)
			service := NewService(Options{Store: store, Transactor: store, NewSecret: fixedSecret("sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")})
			_, err := service.Add(context.Background(), AddInput{TargetUsername: "admin", Name: "公开 Key", RouteStrategyID: "rts_active"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestServiceUpdateAndDeleteProtectOwnershipAndDefaultKey(t *testing.T) {
	store := newPublicAPIKeyServiceStore()
	service := NewService(Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC) },
	})

	other := "other"
	if _, err := service.Update(context.Background(), UpdateInput{TargetUsername: &other, APIKeyID: "key_default", Name: ptrString("New")}); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("cross owner update err = %v, want ErrAPIKeyNotFound", err)
	}

	nextRoute := "rts_other_active"
	if _, err := service.Update(context.Background(), UpdateInput{APIKeyID: "key_default", RouteStrategyID: &nextRoute}); !errors.Is(err, ErrDefaultAPIKeyRouteStrategyChange) {
		t.Fatalf("default route update err = %v, want ErrDefaultAPIKeyRouteStrategyChange", err)
	}

	if _, err := service.Delete(context.Background(), DeleteInput{APIKeyID: "key_default"}); !errors.Is(err, ErrDefaultAPIKeyDelete) {
		t.Fatalf("default delete err = %v, want ErrDefaultAPIKeyDelete", err)
	}

	name := "更新 Key"
	updated, err := service.Update(context.Background(), UpdateInput{
		APIKeyID: "key_normal",
		Name:     &name,
		QuotaLimits: NewJSONValue(map[string]any{
			"total": map[string]any{"enabled": true, "limit": json.Number("300")},
		}, true),
	})
	if err != nil {
		t.Fatalf("update normal: %v", err)
	}
	if updated.Action != "updated" || updated.APIKey == nil || updated.APIKey.Key != "" || updated.APIKey.Name != name {
		t.Fatalf("updated response = %+v", updated)
	}

	deleted, err := service.Delete(context.Background(), DeleteInput{APIKeyID: "key_normal"})
	if err != nil {
		t.Fatalf("delete normal: %v", err)
	}
	if deleted.Action != "deleted" || deleted.APIKey == nil || deleted.APIKey.Key != "" {
		t.Fatalf("deleted response = %+v", deleted)
	}
	if _, ok := store.keys["key_normal"]; ok {
		t.Fatalf("key_normal should be deleted")
	}
}

func TestServiceRejectsInvalidQuotaScheduleAndExpiresAt(t *testing.T) {
	tests := []struct {
		name  string
		input AddInput
		err   error
	}{
		{
			name: "invalid quota disabled flag",
			input: AddInput{
				TargetUsername:  "admin",
				Name:            "quota",
				RouteStrategyID: "rts_active",
				QuotaLimits:     NewJSONValue(map[string]any{"daily": map[string]any{"enabled": false, "limit": json.Number("1")}}, true),
			},
			err: ErrInvalidQuotaLimits,
		},
		{
			name: "invalid schedule timezone",
			input: AddInput{
				TargetUsername:  "admin",
				Name:            "schedule",
				RouteStrategyID: "rts_active",
				AvailabilitySchedule: NewJSONValue(map[string]any{
					"enabled": true, "mode": "allow_windows", "timezone": "Invalid/Timezone",
					"windows": []any{map[string]any{"daysOfWeek": []any{json.Number("2")}, "start": "09:00", "end": "18:00"}},
				}, true),
			},
			err: ErrInvalidAvailabilitySchedule,
		},
		{
			name: "invalid expires at",
			input: AddInput{
				TargetUsername:  "admin",
				Name:            "expires",
				RouteStrategyID: "rts_active",
				ExpiresAt:       ptrString("tomorrow"),
			},
			err: ErrInvalidExpiresAt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPublicAPIKeyServiceStore()
			service := NewService(Options{Store: store, Transactor: store, NewSecret: fixedSecret("sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")})
			_, err := service.Add(context.Background(), tt.input)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
		})
	}
}

type publicAPIKeyServiceStore struct {
	targetsByUsername map[string]port.PublicGroupTarget
	targetsByID       map[string]port.PublicGroupTarget
	routes            map[string]port.PublicAPIKeyRouteStrategyRef
	keys              map[string]port.PublicAPIKeySummary

	createInput port.PublicAPIKeyCreateInput
	updateInput port.PublicAPIKeyUpdateInput
}

func newPublicAPIKeyServiceStore() *publicAPIKeyServiceStore {
	admin := port.PublicGroupTarget{ID: "sys_admin", Username: "admin", DisplayName: "管理员", Status: "active"}
	other := port.PublicGroupTarget{ID: "sys_other", Username: "other", DisplayName: "其他", Status: "active"}
	store := &publicAPIKeyServiceStore{
		targetsByUsername: map[string]port.PublicGroupTarget{"admin": admin, "other": other},
		targetsByID:       map[string]port.PublicGroupTarget{admin.ID: admin, other.ID: other},
		routes: map[string]port.PublicAPIKeyRouteStrategyRef{
			"sys_admin/rts_active": {ID: "rts_active", SystemAccountID: "sys_admin", Name: "公开策略", Mode: port.PublicRouteStrategyModeNormal, Status: port.PublicRouteStrategyStatusActive},
		},
		keys: map[string]port.PublicAPIKeySummary{},
	}
	store.keys["key_default"] = port.PublicAPIKeySummary{
		ID: "key_default", SystemAccountID: "sys_admin", Name: "默认 Key", RouteStrategyID: "rts_active", RouteStrategyName: "公开策略",
		RouteStrategyMode: port.PublicRouteStrategyModeNormal, RouteStrategyStatus: port.PublicRouteStrategyStatusActive,
		Status: port.PublicAPIKeyStatusActive, IsDefault: true, KeyPrefix: "sk-defau", KeySuffix: "default",
		CreatedAt: time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC),
	}
	store.keys["key_normal"] = port.PublicAPIKeySummary{
		ID: "key_normal", SystemAccountID: "sys_admin", Name: "普通 Key", RouteStrategyID: "rts_active", RouteStrategyName: "公开策略",
		RouteStrategyMode: port.PublicRouteStrategyModeNormal, RouteStrategyStatus: port.PublicRouteStrategyStatusActive,
		Status: port.PublicAPIKeyStatusActive, KeyPrefix: "sk-normal", KeySuffix: "normal",
		CreatedAt: time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC),
	}
	return store
}

func (s *publicAPIKeyServiceStore) PublicAPIKeyInTx(ctx context.Context, fn func(ctx context.Context, store port.PublicAPIKeyStore) error) error {
	return fn(ctx, s)
}

func (s *publicAPIKeyServiceStore) FindPublicAPIKeyTargetByUsername(_ context.Context, username string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByUsername[strings.TrimSpace(username)]
	return target, ok, nil
}

func (s *publicAPIKeyServiceStore) FindPublicAPIKeyTargetByID(_ context.Context, id string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByID[id]
	return target, ok, nil
}

func (s *publicAPIKeyServiceStore) ListPublicAPIKeys(_ context.Context, input port.PublicAPIKeyListInput) (port.PublicAPIKeyListPage, error) {
	items := []port.PublicAPIKeySummary{}
	for _, item := range s.keys {
		if item.SystemAccountID == input.SystemAccountID {
			items = append(items, item)
		}
	}
	return port.PublicAPIKeyListPage{Items: items, Page: 1, PageSize: 50, PageUpperBound: len(items)}, nil
}

func (s *publicAPIKeyServiceStore) FindPublicAPIKeyByID(_ context.Context, apiKeyID string) (port.PublicAPIKeySummary, bool, error) {
	key, ok := s.keys[apiKeyID]
	return key, ok, nil
}

func (s *publicAPIKeyServiceStore) FindPublicAPIKeyRouteStrategy(_ context.Context, systemAccountID string, routeStrategyID string) (port.PublicAPIKeyRouteStrategyRef, bool, error) {
	route, ok := s.routes[systemAccountID+"/"+routeStrategyID]
	return route, ok, nil
}

func (s *publicAPIKeyServiceStore) CreatePublicAPIKey(_ context.Context, input port.PublicAPIKeyCreateInput) (port.PublicAPIKeySummary, error) {
	s.createInput = input
	route := s.routes[input.SystemAccountID+"/"+input.RouteStrategyID]
	summary := port.PublicAPIKeySummary{
		ID: input.ID, SystemAccountID: input.SystemAccountID, Name: input.Name, Description: input.Description,
		RouteStrategyID: input.RouteStrategyID, RouteStrategyName: route.Name, RouteStrategyMode: route.Mode, RouteStrategyStatus: route.Status,
		Status: input.Status, KeyPrefix: input.KeyPrefix, KeySuffix: input.KeySuffix, ExpiresAt: input.ExpiresAt,
		QuotaLimitsJSON: input.QuotaLimitsJSON, AvailabilityScheduleJSON: input.AvailabilityScheduleJSON, AvailabilityScheduleNextCheckAt: input.AvailabilityScheduleNextCheckAt,
		CreatedAt: input.Now, UpdatedAt: input.Now,
	}
	s.keys[input.ID] = summary
	return summary, nil
}

func (s *publicAPIKeyServiceStore) UpdatePublicAPIKey(_ context.Context, input port.PublicAPIKeyUpdateInput) (port.PublicAPIKeySummary, bool, error) {
	s.updateInput = input
	current, ok := s.keys[input.ID]
	if !ok {
		return port.PublicAPIKeySummary{}, false, nil
	}
	route := s.routes[input.SystemAccountID+"/"+input.RouteStrategyID]
	current.Name = input.Name
	current.Description = input.Description
	current.RouteStrategyID = input.RouteStrategyID
	current.RouteStrategyName = route.Name
	current.RouteStrategyMode = route.Mode
	current.RouteStrategyStatus = route.Status
	current.Status = input.Status
	current.ExpiresAt = input.ExpiresAt
	current.QuotaLimitsJSON = input.QuotaLimitsJSON
	current.AvailabilityScheduleJSON = input.AvailabilityScheduleJSON
	current.AvailabilityScheduleNextCheckAt = input.AvailabilityScheduleNextCheckAt
	current.UpdatedAt = input.Now
	s.keys[input.ID] = current
	return current, true, nil
}

func (s *publicAPIKeyServiceStore) DeletePublicAPIKey(_ context.Context, apiKeyID string, _ string) (bool, error) {
	if _, ok := s.keys[apiKeyID]; !ok {
		return false, nil
	}
	delete(s.keys, apiKeyID)
	return true, nil
}

func fixedID(id string) func(string) string {
	return func(string) string { return id }
}

func fixedSecret(secret string) func() (string, error) {
	return func() (string, error) { return secret, nil }
}

func ptrString(value string) *string {
	return &value
}
