package managementexternalintegrationsources

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

func TestTokenUpdateServicePreservesPresenceAndNormalizesValues(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 9, 10, 123456789, time.FixedZone("UTC+8", 8*60*60))
	store := &externalIntegrationSourceTokenUpdateStoreStub{result: validTokenUpdateStoreResult(now.UTC())}
	service := NewTokenUpdateServiceWithOptions(TokenUpdateServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	result, err := service.Update(context.Background(), TokenUpdateInput{
		SourceID:     "\ufeff source_1 \u3000",
		TokenID:      "\u3000 token_1 \ufeff",
		HasName:      true,
		Name:         "\ufeff New Token \u3000",
		HasStatus:    true,
		Status:       publicapi.TokenStatusRevoked,
		HasScopes:    true,
		Scopes:       []any{publicapi.ScopeGroupListRead, publicapi.ScopeAPIKeyListRead, publicapi.ScopeGroupListRead},
		HasExpiresAt: true,
		ExpiresAt:    "2026-08-01T02:03:04.567Z",
	})
	if err != nil {
		t.Fatalf("update external source token: %v", err)
	}
	wantExpiresAt := time.Date(2026, 8, 1, 2, 3, 4, 567000000, time.UTC)
	want := port.ManagementExternalIntegrationSourceTokenUpdateInput{
		SourceID:     "source_1",
		TokenID:      "token_1",
		HasName:      true,
		Name:         "New Token",
		HasStatus:    true,
		Status:       publicapi.TokenStatusRevoked,
		HasScopes:    true,
		ScopesJSON:   `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]`,
		HasExpiresAt: true,
		ExpiresAt:    &wantExpiresAt,
		UpdatedAt:    now.UTC(),
	}
	if store.calls != 1 || !reflect.DeepEqual(store.input, want) {
		t.Fatalf("store calls=%d input=%#v, want %#v", store.calls, store.input, want)
	}
	if !result.Committed || result.Before.ID != "token_1" || result.After.ID != "token_1" ||
		result.Before.Status != publicapi.TokenStatusActive || result.After.Status != publicapi.TokenStatusRevoked {
		t.Fatalf("token update result = %#v", result)
	}
}

func TestTokenUpdateServiceAllowsEmptyPatchAndRefreshesUpdatedAt(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	store := &externalIntegrationSourceTokenUpdateStoreStub{result: validTokenUpdateStoreResult(now)}

	result, err := NewTokenUpdateServiceWithOptions(TokenUpdateServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	}).Update(context.Background(), TokenUpdateInput{SourceID: "source_1", TokenID: "token_1"})
	if err != nil {
		t.Fatalf("empty token patch: %v", err)
	}
	if store.calls != 1 || store.input.HasName || store.input.HasStatus || store.input.HasScopes ||
		store.input.HasExpiresAt || store.input.UpdatedAt != now || !result.Committed {
		t.Fatalf("empty patch store input=%#v result=%#v", store.input, result)
	}
}

func TestTokenUpdateServicePreservesExplicitExpiresAtNull(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 30, 0, 0, time.UTC)
	store := &externalIntegrationSourceTokenUpdateStoreStub{result: validTokenUpdateStoreResult(now)}

	_, err := NewTokenUpdateServiceWithOptions(TokenUpdateServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	}).Update(context.Background(), TokenUpdateInput{
		SourceID: "source_1", TokenID: "token_1", HasExpiresAt: true, ExpiresAt: nil,
	})
	if err != nil {
		t.Fatalf("clear token expiresAt: %v", err)
	}
	if !store.input.HasExpiresAt || store.input.ExpiresAt != nil {
		t.Fatalf("expiresAt presence lost: %#v", store.input)
	}
}

func TestTokenUpdateServiceValidationUsesRequiredOrder(t *testing.T) {
	tests := []struct {
		name  string
		input TokenUpdateInput
		want  string
	}{
		{
			name:  "source id before token id and fields",
			input: TokenUpdateInput{TokenID: "", HasName: true, Name: "", HasStatus: true, Status: "paused"},
			want:  "来源系统不存在",
		},
		{
			name:  "token id before fields",
			input: TokenUpdateInput{SourceID: "source_1", HasName: true, Name: "", HasStatus: true, Status: "paused"},
			want:  "Token 不存在",
		},
		{
			name:  "name before status scopes and expires",
			input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasName: true, Name: "\ufeff", HasStatus: true, Status: "paused", HasScopes: true, Scopes: []any{"unknown"}, HasExpiresAt: true, ExpiresAt: "invalid"},
			want:  "Token 名称不能为空",
		},
		{
			name:  "status before scopes and expires",
			input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasStatus: true, Status: "paused", HasScopes: true, Scopes: []any{"unknown"}, HasExpiresAt: true, ExpiresAt: "invalid"},
			want:  `来源系统 token 状态无效: "paused"`,
		},
		{
			name:  "scopes before expires",
			input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasScopes: true, Scopes: []any{"unknown"}, HasExpiresAt: true, ExpiresAt: "invalid"},
			want:  "来源系统 scope 不受支持：unknown",
		},
		{
			name:  "expires last",
			input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasExpiresAt: true, ExpiresAt: "2026-08-01"},
			want:  "过期时间无效",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceTokenUpdateStoreStub{}
			_, err := NewTokenUpdateService(store).Update(context.Background(), test.input)
			if err == nil || !IsTokenUpdateValidationError(err) || err.Error() != test.want {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
			if store.calls != 0 {
				t.Fatalf("invalid update reached store: %d", store.calls)
			}
		})
	}
}

func TestTokenUpdateServiceValidatesNameByECMAScriptTrimAndUTF16Length(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "\ufeff\u3000", want: "Token 名称不能为空"},
		{name: "over 80 UTF-16", value: strings.Repeat("😀", 40) + "a", want: "Token 名称不能超过 80 个字符"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceTokenUpdateStoreStub{}
			_, err := NewTokenUpdateService(store).Update(context.Background(), TokenUpdateInput{
				SourceID: "source_1", TokenID: "token_1", HasName: true, Name: test.value,
			})
			if err == nil || !IsTokenUpdateValidationError(err) || err.Error() != test.want || store.calls != 0 {
				t.Fatalf("name validation error=%v calls=%d", err, store.calls)
			}
		})
	}
}

func TestTokenUpdateServiceRejectsBuiltInIDsBeforeValidationAndStore(t *testing.T) {
	tests := []TokenUpdateInput{
		{SourceID: publicapi.BuiltInTestSourceID, TokenID: "token_1", HasName: true, Name: ""},
		{SourceID: "source_1", TokenID: publicapi.BuiltInTestTokenID, HasName: true, Name: ""},
	}
	for _, input := range tests {
		store := &externalIntegrationSourceTokenUpdateStoreStub{}
		_, err := NewTokenUpdateService(store).Update(context.Background(), input)
		if !errors.Is(err, ErrBuiltInTokenUpdateRestricted) || err.Error() != "内置测试 Token 不支持编辑" {
			t.Fatalf("built-in error = %v", err)
		}
		if store.calls != 0 {
			t.Fatalf("built-in update reached store: %d", store.calls)
		}
	}
}

func TestTokenUpdateServiceMapsStoreErrors(t *testing.T) {
	storeFailure := errors.New("store unavailable")
	tests := []struct {
		name     string
		storeErr error
		wantErr  error
	}{
		{name: "source not found", storeErr: port.ErrManagementExternalIntegrationSourceNotFound, wantErr: ErrTokenNotFound},
		{name: "token not found", storeErr: port.ErrManagementExternalIntegrationSourceTokenNotFound, wantErr: ErrTokenNotFound},
		{name: "built in", storeErr: port.ErrManagementExternalIntegrationSourceBuiltInTokenUpdateRestricted, wantErr: ErrBuiltInTokenUpdateRestricted},
		{name: "other", storeErr: storeFailure, wantErr: storeFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceTokenUpdateStoreStub{err: test.storeErr}
			result, err := NewTokenUpdateService(store).Update(context.Background(), TokenUpdateInput{SourceID: "source_1", TokenID: "token_1"})
			if !errors.Is(err, test.wantErr) || store.calls != 1 || result.Committed {
				t.Fatalf("error=%v calls=%d result=%#v", err, store.calls, result)
			}
		})
	}
}

func TestTokenUpdateServiceCarriesRevokedAtStateMachineContract(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	previous := now.Add(-time.Hour)
	tests := []struct {
		name          string
		input         TokenUpdateInput
		beforeStatus  string
		beforeRevoked *time.Time
		afterStatus   string
		afterRevoked  *time.Time
	}{
		{name: "non-revoked to revoked sets now", input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasStatus: true, Status: publicapi.TokenStatusRevoked}, beforeStatus: publicapi.TokenStatusActive, afterStatus: publicapi.TokenStatusRevoked, afterRevoked: &now},
		{name: "already revoked without status preserves existing", input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1"}, beforeStatus: publicapi.TokenStatusRevoked, beforeRevoked: &previous, afterStatus: publicapi.TokenStatusRevoked, afterRevoked: &previous},
		{name: "already revoked with revoked status preserves nil", input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasStatus: true, Status: publicapi.TokenStatusRevoked}, beforeStatus: publicapi.TokenStatusRevoked, afterStatus: publicapi.TokenStatusRevoked},
		{name: "revoked to active clears", input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasStatus: true, Status: publicapi.TokenStatusActive}, beforeStatus: publicapi.TokenStatusRevoked, beforeRevoked: &previous, afterStatus: publicapi.TokenStatusActive},
		{name: "revoked to disabled clears", input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1", HasStatus: true, Status: publicapi.TokenStatusDisabled}, beforeStatus: publicapi.TokenStatusRevoked, beforeRevoked: &previous, afterStatus: publicapi.TokenStatusDisabled},
		{name: "non-revoked without status clears anomalous residue", input: TokenUpdateInput{SourceID: "source_1", TokenID: "token_1"}, beforeStatus: publicapi.TokenStatusActive, beforeRevoked: &previous, afterStatus: publicapi.TokenStatusActive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored := validTokenUpdateStoreResult(now)
			stored.BeforeToken.Status = test.beforeStatus
			stored.BeforeToken.RevokedAt = test.beforeRevoked
			stored.AfterToken.Status = test.afterStatus
			stored.AfterToken.RevokedAt = test.afterRevoked
			store := &externalIntegrationSourceTokenUpdateStoreStub{result: stored}
			result, err := NewTokenUpdateServiceWithOptions(TokenUpdateServiceOptions{
				Store: store,
				Now:   func() time.Time { return now },
			}).Update(context.Background(), test.input)
			if err != nil {
				t.Fatalf("update token state: %v", err)
			}
			if store.input.HasStatus != test.input.HasStatus || store.input.Status != test.input.Status || store.input.UpdatedAt != now {
				t.Fatalf("state machine input = %#v", store.input)
			}
			assertOptionalRFC3339Time(t, result.Before.RevokedAt, test.beforeRevoked)
			assertOptionalRFC3339Time(t, result.After.RevokedAt, test.afterRevoked)
		})
	}
}

func TestTokenUpdateServiceReturnsMappingErrorsBeforeCommit(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*port.ManagementExternalIntegrationSourceTokenUpdateResult)
	}{
		{name: "before", configure: func(result *port.ManagementExternalIntegrationSourceTokenUpdateResult) {
			result.BeforeToken.ScopesJSON = "{"
		}},
		{name: "after", configure: func(result *port.ManagementExternalIntegrationSourceTokenUpdateResult) {
			result.AfterToken.ScopesJSON = "{"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored := validTokenUpdateStoreResult(time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC))
			test.configure(&stored)
			store := &externalIntegrationSourceTokenUpdateStoreStub{result: stored}
			result, err := NewTokenUpdateService(store).Update(context.Background(), TokenUpdateInput{SourceID: "source_1", TokenID: "token_1"})
			if err == nil || store.calls != 1 || store.validateCalls != 1 || result.Committed {
				t.Fatalf("mapping error=%v calls=%d validate=%d result=%#v", err, store.calls, store.validateCalls, result)
			}
		})
	}
}

func TestTokenUpdateServiceRequiresStore(t *testing.T) {
	_, err := NewTokenUpdateService(nil).Update(context.Background(), TokenUpdateInput{SourceID: "source_1", TokenID: "token_1"})
	if err == nil {
		t.Fatal("token update without store succeeded")
	}
}

func validTokenUpdateStoreResult(now time.Time) port.ManagementExternalIntegrationSourceTokenUpdateResult {
	before := validPrimaryTokenRow("source_1", "token_1", now.Add(-time.Hour))
	after := before
	after.Name = "New Token"
	after.Status = publicapi.TokenStatusRevoked
	after.ScopesJSON = `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]`
	after.UpdatedAt = now
	return port.ManagementExternalIntegrationSourceTokenUpdateResult{BeforeToken: before, AfterToken: after}
}

func assertOptionalRFC3339Time(t *testing.T, got *string, want *time.Time) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("time = %q, want nil", *got)
		}
		return
	}
	if got == nil || *got != want.UTC().Format(javaScriptISOStringLayout) {
		t.Fatalf("time = %v, want %q", got, want.UTC().Format(javaScriptISOStringLayout))
	}
}

type externalIntegrationSourceTokenUpdateStoreStub struct {
	input         port.ManagementExternalIntegrationSourceTokenUpdateInput
	result        port.ManagementExternalIntegrationSourceTokenUpdateResult
	err           error
	calls         int
	validateCalls int
}

func (s *externalIntegrationSourceTokenUpdateStoreStub) UpdateManagementExternalIntegrationSourceToken(
	_ context.Context,
	input port.ManagementExternalIntegrationSourceTokenUpdateInput,
	validate func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error,
) (port.ManagementExternalIntegrationSourceTokenUpdateResult, error) {
	s.calls++
	s.input = input
	if s.err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, s.err
	}
	s.validateCalls++
	if err := validate(s.result); err != nil {
		return port.ManagementExternalIntegrationSourceTokenUpdateResult{}, err
	}
	return s.result, nil
}

var _ port.ManagementExternalIntegrationSourceTokenUpdater = (*externalIntegrationSourceTokenUpdateStoreStub)(nil)
