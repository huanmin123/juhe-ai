package managementapikeys

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceRevealDecryptsScopedSecretWithoutExposingCiphertext(t *testing.T) {
	codec := secretcrypto.NewJSONCodec("management-api-key-secret-test")
	encrypted, err := codec.EncryptJSON(map[string]any{"key": "sk-revealed-secret"})
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	store := &managementAPIKeySecretStoreStub{
		secret: port.ManagementAPIKeySecretRow{
			ID:                 "key_1",
			SystemAccountID:    "sys_owner",
			Name:               "生产 Key",
			KeyPrefix:          "sk-revea",
			KeySuffix:          "d-secret",
			KeySecretEncrypted: &encrypted,
		},
		secretFound: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:  store,
		SecretStore: store,
		Secret:      "management-api-key-secret-test",
	})

	result, err := service.Reveal(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             " key_1 ",
		SystemAccountID:      " all ",
	})
	if err != nil {
		t.Fatalf("Reveal() error = %v", err)
	}
	if store.findInput.APIKeyID != "key_1" || store.findInput.SystemAccountID != "" {
		t.Fatalf("find input = %+v", store.findInput)
	}
	if result.Key != "sk-revealed-secret" ||
		result.APIKeyID != "key_1" ||
		result.OwnerSystemAccountID != "sys_owner" ||
		result.Name != "生产 Key" ||
		result.KeyMarker != "sk-revea...d-secret" {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceRevealForcesSelfOwnerAndRejectsMissingOrInvalidCiphertext(t *testing.T) {
	codec := secretcrypto.NewJSONCodec("management-api-key-secret-test")
	valid, err := codec.EncryptJSON(map[string]any{"key": "sk-self-secret"})
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}

	t.Run("self ignores forged owner", func(t *testing.T) {
		store := &managementAPIKeySecretStoreStub{
			secret: port.ManagementAPIKeySecretRow{
				ID:                 "key_self",
				SystemAccountID:    "sys_current",
				Name:               "个人 Key",
				KeyPrefix:          "sk-self-",
				KeySuffix:          "f-secret",
				KeySecretEncrypted: &valid,
			},
			secretFound: true,
		}
		service := NewServiceWithOptions(ServiceOptions{
			ListReader:  store,
			SecretStore: store,
			Secret:      "management-api-key-secret-test",
		})

		_, err := service.Reveal(context.Background(), SecretInput{
			ActorSystemAccountID: " sys_current ",
			ActorRole:            "admin",
			APIKeyID:             "key_self",
			SystemAccountID:      "sys_forged",
			SelfOnly:             true,
		})
		if err != nil {
			t.Fatalf("Reveal() error = %v", err)
		}
		if store.findInput.SystemAccountID != "sys_current" {
			t.Fatalf("find owner = %q, want actor owner", store.findInput.SystemAccountID)
		}
	})

	tests := []struct {
		name      string
		row       port.ManagementAPIKeySecretRow
		found     bool
		wantError error
	}{
		{
			name:      "missing row",
			found:     false,
			wantError: ErrAPIKeyNotFound,
		},
		{
			name: "null ciphertext",
			row: port.ManagementAPIKeySecretRow{
				ID: "key_null",
			},
			found:     true,
			wantError: ErrAPIKeySecretUnavailable,
		},
		{
			name: "invalid ciphertext",
			row: port.ManagementAPIKeySecretRow{
				ID:                 "key_invalid",
				KeySecretEncrypted: ptrSecretText("not-ciphertext"),
			},
			found:     true,
			wantError: ErrAPIKeySecretUnavailable,
		},
		{
			name: "missing key field",
			row: port.ManagementAPIKeySecretRow{
				ID:                 "key_missing_field",
				KeySecretEncrypted: encryptedSecretFixture(t, "management-api-key-secret-test", map[string]any{"token": "x"}),
			},
			found:     true,
			wantError: ErrAPIKeySecretUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementAPIKeySecretStoreStub{secret: test.row, secretFound: test.found}
			service := NewServiceWithOptions(ServiceOptions{
				ListReader:  store,
				SecretStore: store,
				Secret:      "management-api-key-secret-test",
			})

			_, err := service.Reveal(context.Background(), SecretInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "super_admin",
				APIKeyID:             "key_target",
			})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Reveal() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestServiceRefreshLocksUpdatesCommitsThenInvalidatesAndLoadsPreaggregatedUsage(t *testing.T) {
	now := time.Date(2026, 7, 11, 5, 6, 7, 0, time.UTC)
	events := []string{}
	store := &managementAPIKeySecretStoreStub{
		refreshRow: port.ManagementAPIKeyListRow{
			ID:                       "key_1",
			SystemAccountID:          "sys_owner",
			SystemAccountName:        "所有者",
			Name:                     "生产 Key",
			KeyPrefix:                "sk-before",
			KeySuffix:                "before",
			Status:                   "active",
			RouteStrategyID:          "route_1",
			RouteStrategyName:        "默认策略",
			RouteStrategyMode:        "normal",
			RouteStrategyStatus:      "active",
			QuotaLimitsJSON:          ptrSecretText(`{"daily":{"enabled":true,"limit":12}}`),
			AvailabilityScheduleJSON: nil,
		},
		refreshFound: true,
		updateFound:  true,
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
		events: &events,
	}
	invalidator := &managementAPIKeyInvalidatorStub{events: &events}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:       store,
		SecretStore:      store,
		SecretTransactor: store,
		Invalidator:      invalidator,
		Secret:           "management-api-key-secret-test",
		Now:              func() time.Time { return now },
		NewSecret:        func() (string, error) { return "sk-refreshed-secret-0123456789", nil },
	})

	result, err := service.Refresh(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             " key_1 ",
		SystemAccountID:      " sys_owner ",
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got, want := events, []string{"tx_begin", "lock", "update", "tx_commit", "validation", "runtime", "quota", "usage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if store.lockInput != (port.ManagementAPIKeySecretScope{APIKeyID: "key_1", SystemAccountID: "sys_owner"}) {
		t.Fatalf("lock input = %+v", store.lockInput)
	}
	if store.updateInput.APIKeyID != "key_1" ||
		store.updateInput.SystemAccountID != "sys_owner" ||
		store.updateInput.KeyHash == "" ||
		store.updateInput.KeyPrefix != "sk-refre" ||
		store.updateInput.KeySuffix != "23456789" ||
		store.updateInput.KeySecretEncrypted == "" ||
		!store.updateInput.UpdatedAt.Equal(now) {
		t.Fatalf("update input = %+v", store.updateInput)
	}
	payload, err := secretcrypto.NewJSONCodec("management-api-key-secret-test").DecryptJSON(store.updateInput.KeySecretEncrypted)
	if err != nil || payload["key"] != "sk-refreshed-secret-0123456789" {
		t.Fatalf("encrypted payload = %#v, err = %v", payload, err)
	}
	if invalidator.calls != 3 ||
		invalidator.runtimeReason != "api_key_secret_refreshed" ||
		invalidator.quotaReason != "api_key_secret_refreshed" ||
		invalidator.quotaAPIKeyID != "key_1" {
		t.Fatalf("invalidator = %+v", invalidator)
	}
	if len(store.usageScopes) != 1 ||
		store.usageScopes[0] != (port.ManagementAPIKeyUsageScope{SystemAccountID: "sys_owner", APIKeyID: "key_1"}) {
		t.Fatalf("usage scopes = %+v", store.usageScopes)
	}
	if result.Key != "sk-refreshed-secret-0123456789" ||
		result.PreviousKeyMarker != "sk-before...before" ||
		result.KeyMarker != "sk-refre...23456789" ||
		result.OwnerSystemAccountID != "sys_owner" ||
		result.ListItem.SystemAccountID != "sys_owner" ||
		result.ListItem.SystemAccountName != "所有者" ||
		result.ListItem.Usage.RequestCount != 9 ||
		result.ListItem.QuotaLimits.Daily == nil ||
		result.ListItem.QuotaLimits.Daily.Limit != 12 {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceRefreshSelfScopeHidesOwnerAndRepairsNullCiphertext(t *testing.T) {
	store := &managementAPIKeySecretStoreStub{
		refreshRow: port.ManagementAPIKeyListRow{
			ID:                "key_self",
			SystemAccountID:   "sys_current",
			SystemAccountName: "当前用户",
			Name:              "个人 Key",
			KeyPrefix:         "sk-before",
			KeySuffix:         "before",
			Status:            "active",
			RouteStrategyID:   "route_self",
		},
		refreshFound: true,
		updateFound:  true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:       store,
		SecretStore:      store,
		SecretTransactor: store,
		Invalidator:      &managementAPIKeyInvalidatorStub{},
		Secret:           "management-api-key-secret-test",
		NewSecret:        func() (string, error) { return "sk-self-refreshed-0123456789", nil },
	})

	result, err := service.Refresh(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_current",
		ActorRole:            "admin",
		APIKeyID:             "key_self",
		SystemAccountID:      "sys_forged",
		SelfOnly:             true,
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if store.lockInput.SystemAccountID != "sys_current" {
		t.Fatalf("lock owner = %q, want actor owner", store.lockInput.SystemAccountID)
	}
	if store.updateInput.KeySecretEncrypted == "" {
		t.Fatal("refresh did not repair missing ciphertext")
	}
	if result.ListItem.SystemAccountID != "" || result.ListItem.SystemAccountName != "" {
		t.Fatalf("self result leaked owner fields: %+v", result.ListItem)
	}
}

func TestServiceRefreshReturnsNotFoundBeforeGeneratingSecret(t *testing.T) {
	generated := 0
	store := &managementAPIKeySecretStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:       store,
		SecretStore:      store,
		SecretTransactor: store,
		Invalidator:      &managementAPIKeyInvalidatorStub{},
		NewSecret: func() (string, error) {
			generated++
			return "sk-unused", nil
		},
	})

	_, err := service.Refresh(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "missing",
	})
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("Refresh() error = %v, want %v", err, ErrAPIKeyNotFound)
	}
	if generated != 0 || store.updateCalls != 0 || store.commits != 0 || store.rollbacks != 1 {
		t.Fatalf("generated=%d updates=%d commits=%d rollbacks=%d", generated, store.updateCalls, store.commits, store.rollbacks)
	}
}

func TestServiceRefreshValidationFailureOccursAfterCommitAndStopsLaterEffects(t *testing.T) {
	events := []string{}
	validationErr := errors.New("validation cache unavailable")
	store := &managementAPIKeySecretStoreStub{
		refreshRow: port.ManagementAPIKeyListRow{
			ID:              "key_1",
			SystemAccountID: "sys_owner",
			Name:            "生产 Key",
			KeyPrefix:       "sk-before",
			KeySuffix:       "before",
			Status:          "active",
			RouteStrategyID: "route_1",
		},
		refreshFound: true,
		updateFound:  true,
		events:       &events,
	}
	invalidator := &managementAPIKeyInvalidatorStub{
		events:        &events,
		validationErr: validationErr,
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:       store,
		SecretStore:      store,
		SecretTransactor: store,
		Invalidator:      invalidator,
		Secret:           "management-api-key-secret-test",
		NewSecret:        func() (string, error) { return "sk-committed-secret-0123456789", nil },
	})

	_, err := service.Refresh(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "key_1",
	})
	if !errors.Is(err, validationErr) {
		t.Fatalf("Refresh() error = %v, want %v", err, validationErr)
	}
	if got, want := events, []string{"tx_begin", "lock", "update", "tx_commit", "validation"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if store.commits != 1 || store.updateInput.KeyHash == "" {
		t.Fatalf("commits=%d update=%+v", store.commits, store.updateInput)
	}
	if invalidator.calls != 1 || store.usageCalls != 0 {
		t.Fatalf("invalidation calls=%d usage calls=%d", invalidator.calls, store.usageCalls)
	}
}

func TestServiceRefreshRuntimeAndQuotaInvalidationAreBestEffort(t *testing.T) {
	store := &managementAPIKeySecretStoreStub{
		refreshRow: port.ManagementAPIKeyListRow{
			ID:              "key_1",
			SystemAccountID: "sys_owner",
			Name:            "生产 Key",
			KeyPrefix:       "sk-before",
			KeySuffix:       "before",
			Status:          "active",
			RouteStrategyID: "route_1",
		},
		refreshFound: true,
		updateFound:  true,
	}
	invalidator := &managementAPIKeyInvalidatorStub{
		runtimeErr: errors.New("runtime unavailable"),
		quotaErr:   errors.New("quota unavailable"),
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:       store,
		SecretStore:      store,
		SecretTransactor: store,
		Invalidator:      invalidator,
		Secret:           "management-api-key-secret-test",
		NewSecret:        func() (string, error) { return "sk-best-effort-secret-0123456789", nil },
	})

	result, err := service.Refresh(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "key_1",
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if result.Key == "" || invalidator.calls != 3 {
		t.Fatalf("result=%+v invalidator=%+v", result, invalidator)
	}
}

func encryptedSecretFixture(t *testing.T, secret string, payload map[string]any) *string {
	t.Helper()
	encrypted, err := secretcrypto.NewJSONCodec(secret).EncryptJSON(payload)
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	return &encrypted
}

func ptrSecretText(value string) *string {
	return &value
}

type managementAPIKeySecretStoreStub struct {
	findInput    port.ManagementAPIKeySecretScope
	lockInput    port.ManagementAPIKeySecretScope
	updateInput  port.ManagementAPIKeySecretUpdateInput
	usageScopes  []port.ManagementAPIKeyUsageScope
	secret       port.ManagementAPIKeySecretRow
	refreshRow   port.ManagementAPIKeyListRow
	usage        []port.ManagementAPIKeyUsageRow
	secretFound  bool
	refreshFound bool
	updateFound  bool
	findErr      error
	lockErr      error
	updateErr    error
	usageErr     error
	events       *[]string
	findCalls    int
	lockCalls    int
	updateCalls  int
	usageCalls   int
	commits      int
	rollbacks    int
}

func (s *managementAPIKeySecretStoreStub) ListManagementAPIKeys(
	context.Context,
	port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	return port.ManagementAPIKeyListPage{}, nil
}

func (s *managementAPIKeySecretStoreStub) ListManagementAPIKeyUsageTotals(
	_ context.Context,
	scopes []port.ManagementAPIKeyUsageScope,
) ([]port.ManagementAPIKeyUsageRow, error) {
	s.usageCalls++
	s.usageScopes = append([]port.ManagementAPIKeyUsageScope(nil), scopes...)
	s.record("usage")
	return s.usage, s.usageErr
}

func (s *managementAPIKeySecretStoreStub) FindManagementAPIKeySecret(
	_ context.Context,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeySecretRow, bool, error) {
	s.findCalls++
	s.findInput = input
	return s.secret, s.secretFound, s.findErr
}

func (s *managementAPIKeySecretStoreStub) LockManagementAPIKeySecretRefreshTarget(
	_ context.Context,
	input port.ManagementAPIKeySecretScope,
) (port.ManagementAPIKeyListRow, bool, error) {
	s.lockCalls++
	s.lockInput = input
	s.record("lock")
	return s.refreshRow, s.refreshFound, s.lockErr
}

func (s *managementAPIKeySecretStoreStub) UpdateManagementAPIKeySecret(
	_ context.Context,
	input port.ManagementAPIKeySecretUpdateInput,
) (bool, error) {
	s.updateCalls++
	s.updateInput = input
	s.record("update")
	if s.updateFound {
		s.refreshRow.KeyPrefix = input.KeyPrefix
		s.refreshRow.KeySuffix = input.KeySuffix
	}
	return s.updateFound, s.updateErr
}

func (s *managementAPIKeySecretStoreStub) ManagementAPIKeySecretInTx(
	ctx context.Context,
	fn func(context.Context, port.ManagementAPIKeySecretStore) error,
) error {
	s.record("tx_begin")
	if err := fn(ctx, s); err != nil {
		s.rollbacks++
		s.record("tx_rollback")
		return err
	}
	s.commits++
	s.record("tx_commit")
	return nil
}

func (s *managementAPIKeySecretStoreStub) record(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

type managementAPIKeyInvalidatorStub struct {
	events        *[]string
	calls         int
	validationErr error
	runtimeErr    error
	quotaErr      error
	runtimeReason string
	quotaReason   string
	quotaAPIKeyID string
}

func (s *managementAPIKeyInvalidatorStub) InvalidateAPIKeyValidationCache(context.Context) error {
	s.calls++
	s.record("validation")
	return s.validationErr
}

func (s *managementAPIKeyInvalidatorStub) InvalidateGatewayRuntime(_ context.Context, reason string) error {
	s.calls++
	s.runtimeReason = reason
	s.record("runtime")
	return s.runtimeErr
}

func (s *managementAPIKeyInvalidatorStub) InvalidateAPIKeyQuotaChanged(
	_ context.Context,
	apiKeyID string,
	reason string,
) error {
	s.calls++
	s.quotaAPIKeyID = apiKeyID
	s.quotaReason = reason
	s.record("quota")
	return s.quotaErr
}

func (s *managementAPIKeyInvalidatorStub) record(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

var _ port.ManagementAPIKeyListReader = (*managementAPIKeySecretStoreStub)(nil)
var _ port.ManagementAPIKeySecretStore = (*managementAPIKeySecretStoreStub)(nil)
var _ port.ManagementAPIKeySecretTransactor = (*managementAPIKeySecretStoreStub)(nil)
