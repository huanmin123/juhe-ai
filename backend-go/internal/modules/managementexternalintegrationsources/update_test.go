package managementexternalintegrationsources

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

var updateTestNow = time.Date(2026, 7, 16, 8, 9, 10, 987654321, time.FixedZone("CST", 8*60*60))

func TestUpdateServiceFullNormalizationAndDetailMapping(t *testing.T) {
	store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
	service := NewUpdateService(store)
	service.now = func() time.Time { return updateTestNow }
	expiresAt := "2026-08-01T02:03:04Z"
	notes := "\u3000更新说明\uFEFF"

	result, err := service.Update(context.Background(), UpdateInput{
		SourceID:      "\uFEFF source_1 \u3000",
		HasName:       true,
		Name:          "\u00A0新来源😀\u2029",
		HasStatus:     true,
		Status:        publicapi.SourceStatusDisabled,
		HasScopes:     true,
		Scopes:        []any{publicapi.ScopeGroupListRead, "\u3000" + publicapi.ScopeAPIKeyListRead + "\uFEFF", publicapi.ScopeGroupListRead},
		HasRateLimits: true,
		RateLimits: []any{
			map[string]any{"windowSeconds": json.Number("60"), "maxRequests": float64(100)},
			map[string]any{"maxRequests": 2, "windowSeconds": int64(1)},
		},
		HasExpiresAt: true,
		ExpiresAt:    expiresAt,
		HasNotes:     true,
		Notes:        notes,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	wantUpdatedAt := updateTestNow.UTC()
	input := store.input
	if input.SourceID != "source_1" || !input.HasName || input.Name != "新来源😀" ||
		!input.HasStatus || input.Status != publicapi.SourceStatusDisabled ||
		!input.HasScopes || input.ScopesJSON != `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]` ||
		!input.HasRateLimits || input.RateLimitsJSON != `[{"windowSeconds":1,"maxRequests":2},{"windowSeconds":60,"maxRequests":100}]` ||
		!input.HasExpiresAt || input.ExpiresAt == nil || input.ExpiresAt.Format("2006-01-02T15:04:05.000Z") != "2026-08-01T02:03:04.000Z" ||
		!input.HasNotes || input.Notes == nil || *input.Notes != "更新说明" ||
		!input.UpdatedAt.Equal(wantUpdatedAt) || input.UpdatedAt.Location() != time.UTC {
		t.Fatalf("store input = %#v", input)
	}
	if !result.Committed || result.Before.ID != "source_1" || result.After.Status != publicapi.SourceStatusDisabled {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Before.Tokens) != 1 || len(result.After.Tokens) != 1 ||
		result.Before.TokenCount != 1 || result.After.ActiveTokenCount != 0 ||
		result.Before.PrimaryToken != nil || result.After.PrimaryToken != nil {
		t.Fatalf("detail token mapping = before %#v after %#v", result.Before, result.After)
	}
}

func TestUpdateServiceEmptyPatchStillCallsStore(t *testing.T) {
	store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
	service := NewUpdateService(store)
	service.now = func() time.Time { return updateTestNow }

	result, err := service.Update(context.Background(), UpdateInput{SourceID: "source_1"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.calls != 1 || store.input.HasName || store.input.HasStatus || store.input.HasScopes ||
		store.input.HasRateLimits || store.input.HasExpiresAt || store.input.HasNotes {
		t.Fatalf("empty patch store input = %#v, calls = %d", store.input, store.calls)
	}
	if !result.Committed {
		t.Fatal("result.Committed = false, want true")
	}
}

func TestUpdateServiceECMAScriptTrimAndUTF16Boundaries(t *testing.T) {
	tests := []struct {
		name      string
		input     UpdateInput
		wantName  string
		wantNotes *string
		wantErr   bool
	}{
		{name: "name 80 UTF-16", input: UpdateInput{SourceID: "source_1", HasName: true, Name: strings.Repeat("😀", 40)}, wantName: strings.Repeat("😀", 40)},
		{name: "name 81 UTF-16", input: UpdateInput{SourceID: "source_1", HasName: true, Name: strings.Repeat("😀", 40) + "a"}, wantErr: true},
		{name: "notes 500 UTF-16", input: UpdateInput{SourceID: "source_1", HasNotes: true, Notes: strings.Repeat("😀", 250)}, wantNotes: stringPointer(strings.Repeat("😀", 250))},
		{name: "notes 501 UTF-16", input: UpdateInput{SourceID: "source_1", HasNotes: true, Notes: strings.Repeat("😀", 250) + "a"}, wantErr: true},
		{name: "ECMAScript whitespace trimmed", input: UpdateInput{SourceID: "\uFEFFsource_1\u3000", HasName: true, Name: "\u2028名称\u00A0", HasNotes: true, Notes: "\u2007备注\u2029"}, wantName: "名称", wantNotes: stringPointer("备注")},
		{name: "non ECMAScript whitespace retained", input: UpdateInput{SourceID: "source_1", HasName: true, Name: "\u200B名称\u200B"}, wantName: "\u200B名称\u200B"},
		{name: "empty notes become nil", input: UpdateInput{SourceID: "source_1", HasNotes: true, Notes: "\u3000\uFEFF"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
			service := NewUpdateService(store)
			service.now = func() time.Time { return updateTestNow }
			_, err := service.Update(context.Background(), test.input)
			if test.wantErr {
				assertUpdateValidationError(t, err)
				if store.calls != 0 {
					t.Fatalf("store calls = %d, want 0", store.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if test.input.HasName && store.input.Name != test.wantName {
				t.Fatalf("name = %q, want %q", store.input.Name, test.wantName)
			}
			if test.input.HasNotes && !reflect.DeepEqual(store.input.Notes, test.wantNotes) {
				t.Fatalf("notes = %#v, want %#v", store.input.Notes, test.wantNotes)
			}
		})
	}
}

func TestUpdateServiceRejectsInvalidPresenceTypesAndValues(t *testing.T) {
	tests := []struct {
		name  string
		input UpdateInput
	}{
		{name: "empty source ID", input: UpdateInput{SourceID: "\uFEFF\u3000"}},
		{name: "empty name", input: UpdateInput{SourceID: "source_1", HasName: true, Name: "\u3000"}},
		{name: "invalid status", input: UpdateInput{SourceID: "source_1", HasStatus: true, Status: " active "}},
		{name: "null scopes", input: UpdateInput{SourceID: "source_1", HasScopes: true, Scopes: nil}},
		{name: "scopes object", input: UpdateInput{SourceID: "source_1", HasScopes: true, Scopes: map[string]any{}}},
		{name: "scope non string", input: UpdateInput{SourceID: "source_1", HasScopes: true, Scopes: []any{1}}},
		{name: "null rate limits", input: UpdateInput{SourceID: "source_1", HasRateLimits: true, RateLimits: nil}},
		{name: "rate limits object", input: UpdateInput{SourceID: "source_1", HasRateLimits: true, RateLimits: map[string]any{}}},
		{name: "expires number", input: UpdateInput{SourceID: "source_1", HasExpiresAt: true, ExpiresAt: 123}},
		{name: "notes number", input: UpdateInput{SourceID: "source_1", HasNotes: true, Notes: 123}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
			service := NewUpdateService(store)
			_, err := service.Update(context.Background(), test.input)
			assertUpdateValidationError(t, err)
			if store.calls != 0 {
				t.Fatalf("store calls = %d, want 0", store.calls)
			}
		})
	}
}

func TestUpdateServiceNormalizesScopeWhitelistDedupeAndSort(t *testing.T) {
	store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
	service := NewUpdateService(store)
	_, err := service.Update(context.Background(), UpdateInput{
		SourceID:  "source_1",
		HasScopes: true,
		Scopes: []any{
			publicapi.ScopeRouteStrategyUpdateWrite,
			"\u00A0" + publicapi.ScopeAPIKeyListRead + "\u3000",
			publicapi.ScopeRouteStrategyUpdateWrite,
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.input.ScopesJSON != `["juhe_ai_public:api_key_list:read","juhe_ai_public:route_strategy_update:write"]` {
		t.Fatalf("ScopesJSON = %s", store.input.ScopesJSON)
	}

	for _, scopes := range []any{
		[]any{""},
		[]any{"\u3000"},
		[]any{"unknown:scope"},
	} {
		store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
		_, err := NewUpdateService(store).Update(context.Background(), UpdateInput{SourceID: "source_1", HasScopes: true, Scopes: scopes})
		assertUpdateValidationError(t, err)
		if store.calls != 0 {
			t.Fatalf("invalid scopes called store: %#v", scopes)
		}
	}
}

func TestUpdateServiceValidatesAndSortsRateLimits(t *testing.T) {
	valid := UpdateInput{
		SourceID:      "source_1",
		HasRateLimits: true,
		RateLimits: []any{
			map[string]any{"windowSeconds": float64(60), "maxRequests": json.Number("100")},
			map[string]any{"windowSeconds": 1, "maxRequests": 2},
		},
	}
	store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
	if _, err := NewUpdateService(store).Update(context.Background(), valid); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.input.RateLimitsJSON != `[{"windowSeconds":1,"maxRequests":2},{"windowSeconds":60,"maxRequests":100}]` {
		t.Fatalf("RateLimitsJSON = %s", store.input.RateLimitsJSON)
	}

	tests := []struct {
		name  string
		value any
	}{
		{name: "more than eight", value: nineUpdateRateLimits()},
		{name: "item not object", value: []any{1}},
		{name: "unknown field", value: []any{map[string]any{"windowSeconds": 1, "maxRequests": 2, "extra": 3}}},
		{name: "missing field", value: []any{map[string]any{"windowSeconds": 1}}},
		{name: "fractional window", value: []any{map[string]any{"windowSeconds": 1.5, "maxRequests": 2}}},
		{name: "window below range", value: []any{map[string]any{"windowSeconds": 0, "maxRequests": 2}}},
		{name: "window above range", value: []any{map[string]any{"windowSeconds": 86401, "maxRequests": 2}}},
		{name: "request below range", value: []any{map[string]any{"windowSeconds": 1, "maxRequests": 0}}},
		{name: "request above range", value: []any{map[string]any{"windowSeconds": 1, "maxRequests": 100001}}},
		{name: "duplicate window", value: []any{map[string]any{"windowSeconds": 1, "maxRequests": 2}, map[string]any{"windowSeconds": 1, "maxRequests": 3}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
			_, err := NewUpdateService(store).Update(context.Background(), UpdateInput{SourceID: "source_1", HasRateLimits: true, RateLimits: test.value})
			assertUpdateValidationError(t, err)
			if store.calls != 0 {
				t.Fatalf("store calls = %d, want 0", store.calls)
			}
		})
	}
}

func TestUpdateServiceStrictDates(t *testing.T) {
	valid := []struct {
		name string
		raw  any
		want *time.Time
	}{
		{name: "null", raw: nil, want: nil},
		{name: "seconds", raw: "2026-08-01T02:03:04Z", want: timePointer(time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC))},
		{name: "milliseconds", raw: "2026-08-01T02:03:04.123Z", want: timePointer(time.Date(2026, 8, 1, 2, 3, 4, 123000000, time.UTC))},
		{name: "ECMAScript whitespace", raw: "\u30002026-08-01T02:03:04Z\uFEFF", want: timePointer(time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC))},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
			_, err := NewUpdateService(store).Update(context.Background(), UpdateInput{SourceID: "source_1", HasExpiresAt: true, ExpiresAt: test.raw})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if !reflect.DeepEqual(store.input.ExpiresAt, test.want) {
				t.Fatalf("ExpiresAt = %#v, want %#v", store.input.ExpiresAt, test.want)
			}
		})
	}

	invalid := []any{
		"", "2026-08-01t02:03:04Z", "2026-08-01T02:03:04+00:00",
		"2026-08-01T02:03:04.1Z", "2026-08-01T02:03:04.1234Z", "2026-02-30T02:03:04Z",
	}
	for _, raw := range invalid {
		store := &externalIntegrationSourceUpdateStoreStub{result: validUpdateStoreResult()}
		_, err := NewUpdateService(store).Update(context.Background(), UpdateInput{SourceID: "source_1", HasExpiresAt: true, ExpiresAt: raw})
		assertUpdateValidationError(t, err)
	}
}

func TestUpdateServiceMapsStoreSentinelsAndPassesUnknownErrors(t *testing.T) {
	unknown := errors.New("postgres unavailable")
	tests := []struct {
		name     string
		storeErr error
		wantErr  error
	}{
		{name: "not found", storeErr: port.ErrManagementExternalIntegrationSourceNotFound, wantErr: ErrNotFound},
		{name: "built in restricted", storeErr: port.ErrManagementExternalIntegrationSourceBuiltInUpdateRestricted, wantErr: ErrBuiltInUpdateRestricted},
		{name: "name exists", storeErr: port.ErrManagementExternalIntegrationSourceNameExists, wantErr: ErrNameExists},
		{name: "unknown", storeErr: unknown, wantErr: unknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceUpdateStoreStub{err: test.storeErr}
			_, err := NewUpdateService(store).Update(context.Background(), UpdateInput{SourceID: "source_1"})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Update() error = %v, want errors.Is %v", err, test.wantErr)
			}
			if test.storeErr == unknown && IsUpdateValidationError(err) {
				t.Fatalf("unknown store error classified as validation: %v", err)
			}
		})
	}
}

func TestUpdateServiceSafeDetailMappingFailsBeforeCommit(t *testing.T) {
	stored := validUpdateStoreResult()
	stored.AfterSource.ScopesJSON = `{}`
	store := &externalIntegrationSourceUpdateStoreStub{result: stored}

	result, err := NewUpdateService(store).Update(context.Background(), UpdateInput{SourceID: "source_1"})
	if err == nil {
		t.Fatal("Update() error = nil, want detail mapping error")
	}
	if result.Committed {
		t.Fatal("result.Committed = true after transaction validation failed")
	}
	if store.validated {
		t.Fatal("store committed after transaction validation failed")
	}
	if IsUpdateValidationError(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("mapping error misclassified: %v", err)
	}
}

func TestUpdateServiceRequiresStore(t *testing.T) {
	_, err := NewUpdateService(nil).Update(context.Background(), UpdateInput{SourceID: "source_1"})
	if err == nil || IsUpdateValidationError(err) {
		t.Fatalf("Update() error = %v, want infrastructure error", err)
	}
}

func assertUpdateValidationError(t *testing.T, err error) {
	t.Helper()
	if err == nil || !IsUpdateValidationError(err) {
		t.Fatalf("error = %T %v, want update validation error", err, err)
	}
}

func validUpdateStoreResult() port.ManagementExternalIntegrationSourceUpdateResult {
	beforeTime := time.Date(2026, 7, 15, 1, 2, 3, 456000000, time.UTC)
	afterTime := time.Date(2026, 7, 16, 1, 2, 3, 789000000, time.UTC)
	before := validSourceRow("source_1", beforeTime)
	before.Status = publicapi.SourceStatusActive
	after := validSourceRow("source_1", afterTime)
	after.Status = publicapi.SourceStatusDisabled
	beforeToken := validPrimaryTokenRow("source_1", "token_1", beforeTime)
	afterToken := validPrimaryTokenRow("source_1", "token_1", afterTime)
	afterToken.Status = publicapi.TokenStatusDisabled
	return port.ManagementExternalIntegrationSourceUpdateResult{
		BeforeSource: before,
		BeforeTokens: []port.ManagementExternalIntegrationSourcePrimaryTokenRow{beforeToken},
		AfterSource:  after,
		AfterTokens:  []port.ManagementExternalIntegrationSourcePrimaryTokenRow{afterToken},
	}
}

func nineUpdateRateLimits() []any {
	values := make([]any, 0, 9)
	for index := 1; index <= 9; index++ {
		values = append(values, map[string]any{"windowSeconds": index, "maxRequests": index})
	}
	return values
}

func stringPointer(value string) *string {
	return &value
}

func timePointer(value time.Time) *time.Time {
	return &value
}

type externalIntegrationSourceUpdateStoreStub struct {
	input     port.ManagementExternalIntegrationSourceUpdateInput
	result    port.ManagementExternalIntegrationSourceUpdateResult
	err       error
	calls     int
	validated bool
}

func (s *externalIntegrationSourceUpdateStoreStub) UpdateManagementExternalIntegrationSource(
	_ context.Context,
	input port.ManagementExternalIntegrationSourceUpdateInput,
	validate func(port.ManagementExternalIntegrationSourceUpdateResult) error,
) (port.ManagementExternalIntegrationSourceUpdateResult, error) {
	s.calls++
	s.input = input
	if s.err != nil {
		return s.result, s.err
	}
	if validate != nil {
		if err := validate(s.result); err != nil {
			return port.ManagementExternalIntegrationSourceUpdateResult{}, err
		}
	}
	s.validated = true
	return s.result, nil
}

var _ port.ManagementExternalIntegrationSourceUpdater = (*externalIntegrationSourceUpdateStoreStub)(nil)
