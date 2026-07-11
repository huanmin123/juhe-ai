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
	ctx, cancel := context.WithCancel(context.Background())
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
		events:      &events,
		afterCommit: cancel,
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

	result, err := service.Refresh(ctx, SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             " key_1 ",
		SystemAccountID:      " sys_owner ",
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("request context error = %v, want canceled", ctx.Err())
	}
	if got, want := events, []string{"tx_begin", "lock", "update", "tx_commit", "validation", "lookup", "runtime", "quota", "usage"}; !reflect.DeepEqual(got, want) {
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
	if invalidator.calls != 4 ||
		invalidator.lookupReason != "api_key_secret_refreshed" ||
		invalidator.lookupAPIKeyID != "key_1" ||
		invalidator.runtimeReason != "api_key_secret_refreshed" ||
		invalidator.quotaReason != "api_key_secret_refreshed" ||
		invalidator.quotaAPIKeyID != "key_1" {
		t.Fatalf("invalidator = %+v", invalidator)
	}
	for name, snapshot := range map[string]managementAPIKeyUpdateContextSnapshot{
		"validation": invalidator.validationContext,
		"lookup":     invalidator.lookupContext,
		"runtime":    invalidator.runtimeContext,
		"quota":      invalidator.quotaContext,
	} {
		if snapshot.err != nil || !snapshot.hasDeadline {
			t.Fatalf("%s context = %+v, want live bounded context", name, snapshot)
		}
		if name != "validation" && !snapshot.deadline.Equal(invalidator.validationContext.deadline) {
			t.Fatalf("%s deadline = %s, validation deadline = %s", name, snapshot.deadline, invalidator.validationContext.deadline)
		}
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

func TestServiceRefreshKeepsCommittedSecretWhenUsageSummaryFails(t *testing.T) {
	events := []string{}
	usageErr := errors.New("usage summary unavailable")
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
		usageErr:     usageErr,
		events:       &events,
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:       store,
		SecretStore:      store,
		SecretTransactor: store,
		Invalidator:      &managementAPIKeyInvalidatorStub{events: &events},
		Secret:           "management-api-key-secret-test",
		NewSecret:        func() (string, error) { return "sk-refreshed-secret-0123456789", nil },
	})

	result, err := service.Refresh(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "key_1",
		SystemAccountID:      "sys_owner",
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil after committed usage enrichment failure", err)
	}
	if !result.Committed ||
		result.Key != "sk-refreshed-secret-0123456789" ||
		result.ID != "key_1" ||
		result.Name != "生产 Key" ||
		result.KeyPrefix != "sk-refre" ||
		result.KeySuffix != "23456789" ||
		result.Status != "active" ||
		result.RouteStrategyID != "route_1" ||
		result.OwnerSystemAccountID != "sys_owner" ||
		result.SystemAccountID != "sys_owner" ||
		result.SystemAccountName != "所有者" ||
		result.PreviousKeyMarker != "sk-before...before" ||
		result.KeyMarker != "sk-refre...23456789" ||
		result.QuotaLimits.Daily == nil ||
		result.QuotaLimits.Daily.Limit != 12 {
		t.Fatalf("Refresh() committed result = %+v", result)
	}
	if result.Usage != (port.ManagementAccountUsageSummary{}) {
		t.Fatalf("Refresh() usage = %+v, want zero-value best-effort summary", result.Usage)
	}
	if store.commits != 1 || store.usageCalls != 1 {
		t.Fatalf("commits=%d usageCalls=%d", store.commits, store.usageCalls)
	}
	if got, want := events, []string{"tx_begin", "lock", "update", "tx_commit", "validation", "lookup", "runtime", "quota", "usage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestServiceRefreshKeepsCommittedSecretWhenUsageSummaryTimesOut(t *testing.T) {
	usageTimeout := 10 * time.Millisecond
	store := &managementAPIKeySecretStoreStub{
		refreshRow: port.ManagementAPIKeyListRow{
			ID:                  "key_1",
			SystemAccountID:     "sys_owner",
			SystemAccountName:   "所有者",
			Name:                "生产 Key",
			KeyPrefix:           "sk-before",
			KeySuffix:           "before",
			Status:              "active",
			RouteStrategyID:     "route_1",
			RouteStrategyName:   "默认策略",
			RouteStrategyMode:   "normal",
			RouteStrategyStatus: "active",
		},
		refreshFound:            true,
		updateFound:             true,
		respectUsageContext:     true,
		waitForUsageContextDone: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:          store,
		SecretStore:         store,
		SecretTransactor:    store,
		Invalidator:         &managementAPIKeyInvalidatorStub{},
		Secret:              "management-api-key-secret-test",
		NewSecret:           func() (string, error) { return "sk-refreshed-secret-0123456789", nil },
		RefreshUsageTimeout: usageTimeout,
	})

	startedAt := time.Now()
	result, err := service.Refresh(context.Background(), SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "key_1",
		SystemAccountID:      "sys_owner",
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil after committed usage timeout", err)
	}
	if !result.Committed ||
		result.Key != "sk-refreshed-secret-0123456789" ||
		result.ID != "key_1" ||
		result.Name != "生产 Key" ||
		result.KeyPrefix != "sk-refre" ||
		result.KeySuffix != "23456789" ||
		result.Status != "active" ||
		result.RouteStrategyID != "route_1" ||
		result.OwnerSystemAccountID != "sys_owner" ||
		result.SystemAccountID != "sys_owner" ||
		result.SystemAccountName != "所有者" {
		t.Fatalf("Refresh() committed result = %+v", result)
	}
	if result.Usage != (port.ManagementAccountUsageSummary{}) {
		t.Fatalf("Refresh() usage = %+v, want zero-value best-effort summary", result.Usage)
	}
	if store.commits != 1 ||
		store.usageCalls != 1 ||
		!errors.Is(store.usageContextErr, context.DeadlineExceeded) ||
		time.Since(startedAt) >= time.Second {
		t.Fatalf(
			"commits=%d usageCalls=%d usageContextErr=%v elapsed=%s",
			store.commits,
			store.usageCalls,
			store.usageContextErr,
			time.Since(startedAt),
		)
	}
}

func TestServiceRefreshUsesIndependentDetachedUsageContextAfterInvalidationDeadline(t *testing.T) {
	invalidationTimeout := 10 * time.Millisecond
	usageTimeout := 20 * time.Millisecond
	events := []string{}
	ctx, cancel := context.WithCancel(context.Background())
	store := &managementAPIKeySecretStoreStub{
		refreshRow: port.ManagementAPIKeyListRow{
			ID:                "key_1",
			SystemAccountID:   "sys_owner",
			SystemAccountName: "所有者",
			Name:              "生产 Key",
			KeyPrefix:         "sk-before",
			KeySuffix:         "before",
			Status:            "active",
			RouteStrategyID:   "route_1",
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
		events:              &events,
		afterCommit:         cancel,
		respectUsageContext: true,
	}
	invalidator := &managementAPIKeyInvalidatorStub{
		events:                  &events,
		waitForQuotaContextDone: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		ListReader:                    store,
		SecretStore:                   store,
		SecretTransactor:              store,
		Invalidator:                   invalidator,
		Secret:                        "management-api-key-secret-test",
		NewSecret:                     func() (string, error) { return "sk-refreshed-secret-0123456789", nil },
		ValidationInvalidationTimeout: invalidationTimeout,
		RefreshUsageTimeout:           usageTimeout,
	})

	startedAt := time.Now()
	result, err := service.Refresh(ctx, SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "key_1",
		SystemAccountID:      "sys_owner",
	})
	if err != nil ||
		!result.Committed ||
		result.ID != "key_1" ||
		result.Key != "sk-refreshed-secret-0123456789" ||
		result.Usage.RequestCount != 9 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("request context error = %v, want canceled", ctx.Err())
	}
	if got, want := events, []string{"tx_begin", "lock", "update", "tx_commit", "validation", "lookup", "runtime", "quota", "usage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !errors.Is(invalidator.quotaContext.err, context.DeadlineExceeded) ||
		!invalidator.quotaContext.hasDeadline ||
		!invalidator.quotaContext.deadline.After(startedAt) ||
		invalidator.quotaContext.deadline.After(startedAt.Add(invalidationTimeout+250*time.Millisecond)) {
		t.Fatalf("quota invalidation context = %+v, startedAt=%s", invalidator.quotaContext, startedAt)
	}
	for name, snapshot := range map[string]managementAPIKeyUpdateContextSnapshot{
		"validation": invalidator.validationContext,
		"lookup":     invalidator.lookupContext,
		"runtime":    invalidator.runtimeContext,
	} {
		if snapshot.err != nil ||
			!snapshot.hasDeadline ||
			!snapshot.deadline.Equal(invalidator.quotaContext.deadline) {
			t.Fatalf("%s invalidation context = %+v, quota context = %+v", name, snapshot, invalidator.quotaContext)
		}
	}
	if store.usageCalls != 1 ||
		store.usageContextErr != nil ||
		!store.usageContextHasDeadline ||
		!store.usageContextDeadline.After(invalidator.quotaContext.deadline) {
		t.Fatalf(
			"usage calls=%d context err=%v deadline=%s hasDeadline=%t, invalidation deadline=%s",
			store.usageCalls,
			store.usageContextErr,
			store.usageContextDeadline,
			store.usageContextHasDeadline,
			invalidator.quotaContext.deadline,
		)
	}
}

func TestNewServiceWithOptionsDefaultsRefreshTimeouts(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{
		Invalidator: &managementAPIKeyInvalidatorStub{},
	})
	validationInvalidationTimeout, refreshUsageTimeout := apiKeyRefreshTimeouts(service.invalidator)
	if validationInvalidationTimeout != 5*time.Second ||
		refreshUsageTimeout != 5*time.Second {
		t.Fatalf(
			"validation invalidation timeout=%s refresh usage timeout=%s",
			validationInvalidationTimeout,
			refreshUsageTimeout,
		)
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
	ctx, cancel := context.WithCancel(context.Background())
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
		afterCommit:  cancel,
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

	result, err := service.Refresh(ctx, SecretInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		APIKeyID:             "key_1",
	})
	if !errors.Is(err, ErrAPIKeyRefreshValidationCacheInvalidation) {
		t.Fatalf("Refresh() error = %v, want %v", err, ErrAPIKeyRefreshValidationCacheInvalidation)
	}
	if errors.Is(err, validationErr) ||
		err.Error() != ErrAPIKeyRefreshValidationCacheInvalidation.Error() {
		t.Fatalf("Refresh() error leaked unstable cause: %v", err)
	}
	if !result.Committed ||
		result.ID != "key_1" ||
		result.OwnerSystemAccountID != "sys_owner" ||
		result.Key == "" {
		t.Fatalf("Refresh() committed result = %+v", result)
	}
	if got, want := events, []string{"tx_begin", "lock", "update", "tx_commit", "validation"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if store.commits != 1 || store.updateInput.KeyHash == "" {
		t.Fatalf("commits=%d update=%+v", store.commits, store.updateInput)
	}
	if invalidator.validationContextErr != nil || !invalidator.validationContextHasDeadline {
		t.Fatalf(
			"validation context err=%v hasDeadline=%t, want live bounded context",
			invalidator.validationContextErr,
			invalidator.validationContextHasDeadline,
		)
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
		lookupErr:  errors.New("lookup unavailable"),
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
	if result.Key == "" || invalidator.calls != 4 {
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
	afterCommit  func()

	respectUsageContext     bool
	waitForUsageContextDone bool
	usageContextErr         error
	usageContextDeadline    time.Time
	usageContextHasDeadline bool
}

func (s *managementAPIKeySecretStoreStub) ListManagementAPIKeys(
	context.Context,
	port.ManagementAPIKeyListInput,
) (port.ManagementAPIKeyListPage, error) {
	return port.ManagementAPIKeyListPage{}, nil
}

func (s *managementAPIKeySecretStoreStub) ListManagementAPIKeyUsageTotals(
	ctx context.Context,
	scopes []port.ManagementAPIKeyUsageScope,
) ([]port.ManagementAPIKeyUsageRow, error) {
	s.usageCalls++
	s.usageScopes = append([]port.ManagementAPIKeyUsageScope(nil), scopes...)
	s.record("usage")
	if s.waitForUsageContextDone {
		<-ctx.Done()
	}
	s.usageContextErr = ctx.Err()
	s.usageContextDeadline, s.usageContextHasDeadline = ctx.Deadline()
	if s.respectUsageContext {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
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
	if s.afterCommit != nil {
		s.afterCommit()
	}
	return nil
}

func (s *managementAPIKeySecretStoreStub) record(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

type managementAPIKeyInvalidatorStub struct {
	events                       *[]string
	calls                        int
	validationErr                error
	lookupErr                    error
	runtimeErr                   error
	quotaErr                     error
	lookupReason                 string
	lookupAPIKeyID               string
	runtimeReason                string
	quotaReason                  string
	quotaAPIKeyID                string
	validationContextErr         error
	validationContextHasDeadline bool
	validationContext            managementAPIKeyUpdateContextSnapshot
	lookupContext                managementAPIKeyUpdateContextSnapshot
	runtimeContext               managementAPIKeyUpdateContextSnapshot
	quotaContext                 managementAPIKeyUpdateContextSnapshot
	waitForQuotaContextDone      bool
}

func (s *managementAPIKeyInvalidatorStub) InvalidateAPIKeyValidationCache(ctx context.Context) error {
	s.calls++
	s.validationContextErr = ctx.Err()
	_, s.validationContextHasDeadline = ctx.Deadline()
	s.validationContext = managementAPIKeyUpdateSnapshot(ctx)
	s.record("validation")
	return s.validationErr
}

func (s *managementAPIKeyInvalidatorStub) InvalidateAPIKeyLookupCache(
	ctx context.Context,
	apiKeyID string,
	reason string,
) error {
	s.calls++
	s.lookupAPIKeyID = apiKeyID
	s.lookupReason = reason
	s.lookupContext = managementAPIKeyUpdateSnapshot(ctx)
	s.record("lookup")
	return s.lookupErr
}

func (s *managementAPIKeyInvalidatorStub) InvalidateGatewayRuntime(ctx context.Context, reason string) error {
	s.calls++
	s.runtimeReason = reason
	s.runtimeContext = managementAPIKeyUpdateSnapshot(ctx)
	s.record("runtime")
	return s.runtimeErr
}

func (s *managementAPIKeyInvalidatorStub) InvalidateAPIKeyQuotaChanged(
	ctx context.Context,
	apiKeyID string,
	reason string,
) error {
	s.calls++
	s.quotaAPIKeyID = apiKeyID
	s.quotaReason = reason
	s.record("quota")
	if s.waitForQuotaContextDone {
		<-ctx.Done()
	}
	s.quotaContext = managementAPIKeyUpdateSnapshot(ctx)
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
