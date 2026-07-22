package managementgroups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOptionsNormalizesInputAndMapsOwnerOptions(t *testing.T) {
	store := &groupOptionStoreStub{
		options: []port.ManagementGroupOption{
			{
				ID:                                 "group_default",
				SystemAccountID:                    "sys_admin",
				SystemAccountName:                  "管理员",
				OwnerSystemAccountID:               "sys_admin",
				OwnerSystemAccountName:             "管理员",
				Name:                               "默认分组",
				ProviderCode:                       "openai",
				Enabled:                            true,
				IsDefault:                          true,
				GroupType:                          "high_concurrency",
				SchedulingPolicy:                   map[string]any{"mode": "balanced_fast"},
				HasActiveManualAuthorizationSource: true,
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{
		SystemAccountID:            " sys_admin ",
		IncludeSystemAccountFields: true,
		IDs:                        []string{" group_default ", "group_default", "", "group_disabled"},
		Keyword:                    " 默认 ",
		ProviderCode:               " openai ",
		Limit:                      500,
		ManageableOnly:             true,
		PreferDefault:              true,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}

	if store.input.SystemAccountID != "sys_admin" ||
		store.input.Keyword != "默认" ||
		store.input.ProviderCode != "openai" ||
		store.input.Limit != 50 ||
		!store.input.ManageableOnly ||
		!store.input.PreferDefault ||
		!store.input.IncludeSystemAccountFields {
		t.Fatalf("store input = %+v", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "group_default" || store.input.IDs[1] != "group_disabled" {
		t.Fatalf("store ids = %#v", store.input.IDs)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.SystemAccountID != "sys_admin" ||
		got.SystemAccountName != "管理员" ||
		got.OwnerSystemAccountID != "sys_admin" ||
		got.OwnerSystemAccountName != "管理员" ||
		got.ProviderCode != "openai" ||
		got.GroupType != "high_concurrency" ||
		got.AccessType != "owner" ||
		!got.Enabled ||
		!got.IsDefault {
		t.Fatalf("option = %+v", got)
	}
	if got.Permissions != ownerPermissions() {
		t.Fatalf("permissions = %+v", got.Permissions)
	}
}

func TestServiceOptionsKeepsAuthorizedGroupReturnPermissionFalseWithoutManualSource(t *testing.T) {
	store := &groupOptionStoreStub{
		options: []port.ManagementGroupOption{
			{
				ID:                   "group_team_authorized",
				OwnerSystemAccountID: "sys_owner",
				Name:                 "团队授权分组",
				ProviderCode:         "openai",
				Enabled:              true,
				GroupType:            "personal",
				AccessType:           "authorized",
				GroupAuthorizationID: "auth_group_team",
				AuthorizationStatus:  "active",
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{SystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %+v", options)
	}
	if options[0].Permissions.CanReturnAuthorization {
		t.Fatalf("authorized permissions = %+v, want canReturnAuthorization=false without active manual source", options[0].Permissions)
	}
}

func TestServiceOptionsMapsAuthorizedOptions(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	store := &groupOptionStoreStub{
		options: []port.ManagementGroupOption{
			{
				ID:                                 "group_authorized",
				OwnerSystemAccountID:               "sys_owner",
				OwnerSystemAccountName:             "所有者",
				Name:                               "授权分组",
				ProviderCode:                       "openai",
				Enabled:                            true,
				GroupType:                          "personal",
				AccessType:                         "authorized",
				GroupAuthorizationID:               "auth_group_1",
				AuthorizationStatus:                "active",
				AuthorizationExpiresAt:             &expiresAt,
				AuthorizationLimits:                map[string]any{"daily": map[string]any{"limit": float64(100)}},
				HasActiveManualAuthorizationSource: true,
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{
		SystemAccountID: "sys_user",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}

	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.AccessType != "authorized" ||
		got.GroupAuthorizationID != "auth_group_1" ||
		got.AuthorizationStatus != "active" ||
		got.AuthorizationExpiresAt == nil ||
		got.AuthorizationLimits["daily"] == nil ||
		got.OwnerSystemAccountName != "所有者" ||
		got.SystemAccountID != "" {
		t.Fatalf("authorized option = %+v", got)
	}
	if got.Permissions.CanAuthorize ||
		got.Permissions.CanDelete ||
		got.Permissions.CanManageAccounts ||
		got.Permissions.CanViewCredentials ||
		!got.Permissions.CanUse ||
		!got.Permissions.CanEdit ||
		!got.Permissions.CanReturnAuthorization ||
		!got.Permissions.CanBindToAPIKey {
		t.Fatalf("authorized permissions = %+v", got.Permissions)
	}
}

func TestAuthorizedGroupCanBindConditions(t *testing.T) {
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		name      string
		enabled   bool
		status    string
		expiresAt *time.Time
		want      bool
	}{
		{name: "active enabled no expiry", enabled: true, status: "active", want: true},
		{name: "active enabled future expiry", enabled: true, status: "active", expiresAt: &future, want: true},
		{name: "disabled", enabled: false, status: "active", expiresAt: &future, want: false},
		{name: "paused", enabled: true, status: "paused", expiresAt: &future, want: false},
		{name: "expired status", enabled: true, status: "expired", expiresAt: &future, want: false},
		{name: "past expiry", enabled: true, status: "active", expiresAt: &past, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canBindAuthorizedGroup(tt.enabled, tt.status, tt.expiresAt); got != tt.want {
				t.Fatalf("canBindAuthorizedGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceOptionsDefaults(t *testing.T) {
	store := &groupOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

func TestServiceAccountOptionsNormalizesInputAndMapsAccountIDs(t *testing.T) {
	store := &groupOptionStoreStub{
		accountOptions: []port.ManagementGroupAccountOption{
			{
				ID:                     "group_default",
				SystemAccountID:        "sys_admin",
				SystemAccountName:      "管理员",
				OwnerSystemAccountID:   "sys_admin",
				OwnerSystemAccountName: "管理员",
				Name:                   "默认分组",
				ProviderCode:           "openai",
				Enabled:                true,
				IsDefault:              true,
				GroupType:              "personal",
				AccountIDs:             []string{"acct_a", "acct_b"},
			},
		},
	}
	service := NewService(store)

	options, err := service.AccountOptions(context.Background(), OptionListInput{
		SystemAccountID:            " sys_admin ",
		IncludeSystemAccountFields: true,
		IDs:                        []string{" group_default ", "group_default", "group_backup"},
		Keyword:                    " 默认 ",
		ProviderCode:               " openai ",
		Limit:                      500,
		ManageableOnly:             true,
		PreferDefault:              true,
	})
	if err != nil {
		t.Fatalf("AccountOptions() error = %v", err)
	}

	if store.accountInput.SystemAccountID != "sys_admin" ||
		store.accountInput.Keyword != "默认" ||
		store.accountInput.ProviderCode != "openai" ||
		store.accountInput.Limit != 50 ||
		!store.accountInput.ManageableOnly ||
		!store.accountInput.PreferDefault ||
		!store.accountInput.IncludeSystemAccountFields {
		t.Fatalf("store account input = %+v", store.accountInput)
	}
	if len(store.accountInput.IDs) != 2 || store.accountInput.IDs[0] != "group_default" || store.accountInput.IDs[1] != "group_backup" {
		t.Fatalf("store account ids = %#v", store.accountInput.IDs)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.ID != "group_default" ||
		got.SystemAccountID != "sys_admin" ||
		got.AccessType != "owner" ||
		len(got.AccountIDs) != 2 ||
		got.AccountIDs[0] != "acct_a" ||
		got.AccountIDs[1] != "acct_b" {
		t.Fatalf("account option = %+v", got)
	}
	if got.Permissions != ownerPermissions() {
		t.Fatalf("permissions = %+v", got.Permissions)
	}
}

func TestServiceAccountOptionsMapsAuthorizedAccountIDsEmpty(t *testing.T) {
	store := &groupOptionStoreStub{
		accountOptions: []port.ManagementGroupAccountOption{
			{
				ID:                     "group_authorized",
				OwnerSystemAccountID:   "sys_owner",
				OwnerSystemAccountName: "所有者",
				Name:                   "授权分组",
				ProviderCode:           "openai",
				Enabled:                true,
				GroupType:              "personal",
				AccessType:             "authorized",
				GroupAuthorizationID:   "auth_group_1",
				AuthorizationStatus:    "paused",
				AccountIDs:             []string{},
			},
		},
	}
	service := NewService(store)

	options, err := service.AccountOptions(context.Background(), OptionListInput{SystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("AccountOptions() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.AccessType != "authorized" ||
		got.GroupAuthorizationID != "auth_group_1" ||
		got.AuthorizationStatus != "paused" ||
		got.OwnerSystemAccountName != "所有者" ||
		len(got.AccountIDs) != 0 ||
		got.Permissions.CanBindToAPIKey ||
		got.Permissions.CanReturnAuthorization {
		t.Fatalf("authorized account option = %+v", got)
	}
}

func TestServiceAccountOptionsMapsAuthorizedReturnPermission(t *testing.T) {
	store := &groupOptionStoreStub{
		accountOptions: []port.ManagementGroupAccountOption{
			{
				ID:                                 "group_authorized",
				OwnerSystemAccountID:               "sys_owner",
				Name:                               "授权分组",
				ProviderCode:                       "openai",
				Enabled:                            true,
				GroupType:                          "personal",
				AccessType:                         "authorized",
				GroupAuthorizationID:               "auth_group_1",
				AuthorizationStatus:                "active",
				HasActiveManualAuthorizationSource: true,
			},
		},
	}
	service := NewService(store)

	options, err := service.AccountOptions(context.Background(), OptionListInput{SystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("AccountOptions() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if !got.Permissions.CanReturnAuthorization {
		t.Fatalf("authorized account permissions = %+v", got.Permissions)
	}
}

func TestServiceCreatePersonalNormalizesDefaultsAndReturnsZeroSummary(t *testing.T) {
	now := time.Date(2026, 7, 10, 20, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	store := &groupOptionStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
		NewID: func(prefix string) string { return prefix + "_fixed" },
	})
	description := "   "
	validPersonalPolicy := 10

	result, err := service.Create(context.Background(), CreateInput{
		SystemAccountID:  " sys_owner ",
		Name:             " 个人分组 ",
		ProviderCode:     " openai ",
		Description:      &description,
		SchedulingPolicy: &SchedulingPolicyInput{DefaultSoftConcurrency: &validPersonalPolicy},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.createCalls != 1 {
		t.Fatalf("CreateManagementGroup() calls = %d, want 1", store.createCalls)
	}
	if store.createInput.ID != "grp_fixed" ||
		store.createInput.SystemAccountID != "sys_owner" ||
		store.createInput.Name != "个人分组" ||
		store.createInput.ProviderCode != "openai" ||
		store.createInput.Description != nil ||
		!store.createInput.Enabled ||
		store.createInput.GroupType != "personal" ||
		store.createInput.SchedulingPolicyJSON != nil ||
		!store.createInput.CreatedAt.Equal(now.UTC()) ||
		!store.createInput.UpdatedAt.Equal(now.UTC()) {
		t.Fatalf("create input = %+v", store.createInput)
	}
	if result.ID != "grp_fixed" ||
		result.SystemAccountID != "" ||
		result.Name != "个人分组" ||
		result.ProviderCode != "openai" ||
		result.Description != nil ||
		!result.Enabled ||
		result.IsDefault ||
		result.GroupType != "personal" ||
		result.SchedulingPolicy != nil ||
		result.AccountIDs == nil ||
		len(result.AccountIDs) != 0 ||
		result.AccountStats != (GroupAccountStats{}) {
		t.Fatalf("create result = %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal(result) error = %v", err)
	}
	for _, field := range []string{
		`"accountIds":[]`,
		`"todayUsage":{"requestCount":0,"inputTokens":0,"outputTokens":0,"cacheReadTokens":0,"cacheReadCost":0,"cacheWriteTokens":0,"cacheWrite1hTokens":0,"cacheWriteCost":0,"thinkingTokens":0,"inputImageTokens":0,"outputImageTokens":0,"totalTokens":0,"totalCost":0}`,
		`"usage":{"requestCount":0,"inputTokens":0,"outputTokens":0,"cacheReadTokens":0,"cacheReadCost":0,"cacheWriteTokens":0,"cacheWrite1hTokens":0,"cacheWriteCost":0,"thinkingTokens":0,"inputImageTokens":0,"outputImageTokens":0,"totalTokens":0,"totalCost":0}`,
	} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("result json = %s, missing %s", encoded, field)
		}
	}
	if strings.Contains(string(encoded), "systemAccountId") || strings.Contains(string(encoded), "schedulingPolicy") {
		t.Fatalf("personal result json = %s, want omitted scoped fields", encoded)
	}
}

func TestServiceCreateHighConcurrencyWritesStableCompletePolicy(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 45, 0, 0, time.UTC)
	store := &groupOptionStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
		NewID: func(prefix string) string { return prefix + "_high" },
	})
	description := " 高并发说明 "
	enabled := false
	defaultSoftConcurrency := 25
	maxQueueWaitMs := 90000
	clientIPConcurrencyLimit := 8
	overflowMode := "queue"
	imageLaneMaxConcurrency := 3

	result, err := service.Create(context.Background(), CreateInput{
		SystemAccountID:            "sys_admin",
		IncludeSystemAccountFields: true,
		Name:                       " 高并发分组 ",
		ProviderCode:               " openai ",
		Description:                &description,
		Enabled:                    &enabled,
		GroupType:                  "high_concurrency",
		SchedulingPolicy: &SchedulingPolicyInput{
			DefaultSoftConcurrency:          &defaultSoftConcurrency,
			MaxQueueWaitMs:                  &maxQueueWaitMs,
			ClientIPConcurrencyLimit:        &clientIPConcurrencyLimit,
			ClientIPConcurrencyOverflowMode: &overflowMode,
			ImageLaneMaxConcurrency:         &imageLaneMaxConcurrency,
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	const wantPolicyJSON = `{"mode":"balanced_fast","defaultSoftConcurrency":25,"fastFirstEnabled":true,"fallbackOnQueueEnabled":true,"breakAffinityOnSoftLimit":true,"breakAffinityOnQueueWaitMs":0,"slowRequestThresholdMs":30000,"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,"recentTimeoutPenaltyThreshold":2,"maxQueueWaitMs":90000,"maxQueueSize":1000,"perApiKeyQueueLimit":1000,"clientIpConcurrencyLimit":8,"clientIpConcurrencyOverflowMode":"queue","imageLaneMaxConcurrency":3}`
	if store.createInput.SchedulingPolicyJSON == nil || *store.createInput.SchedulingPolicyJSON != wantPolicyJSON {
		t.Fatalf("scheduling policy json = %v, want %s", store.createInput.SchedulingPolicyJSON, wantPolicyJSON)
	}
	if store.createInput.Description == nil || *store.createInput.Description != "高并发说明" || store.createInput.Enabled {
		t.Fatalf("create input = %+v", store.createInput)
	}
	if result.SystemAccountID != "sys_admin" ||
		result.SchedulingPolicy == nil ||
		result.SchedulingPolicy.Mode != "balanced_fast" ||
		result.SchedulingPolicy.DefaultSoftConcurrency != 25 ||
		result.SchedulingPolicy.MaxQueueWaitMs != 90000 ||
		result.SchedulingPolicy.ClientIPConcurrencyLimit != 8 ||
		result.SchedulingPolicy.ClientIPConcurrencyOverflowMode != "queue" ||
		result.SchedulingPolicy.ImageLaneMaxConcurrency != 3 ||
		result.Enabled {
		t.Fatalf("create result = %+v", result)
	}
}

func TestServiceCreateDefaultIDUsesGroupPrefixAndCompactUUID(t *testing.T) {
	store := &groupOptionStoreStub{}
	service := NewService(store)

	result, err := service.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !strings.HasPrefix(result.ID, "grp_") ||
		len(result.ID) != len("grp_")+32 ||
		strings.Contains(result.ID, "-") {
		t.Fatalf("group id = %q, want grp_ plus 32 UUID hex characters", result.ID)
	}
}

func TestServiceCreateValidationErrorsSkipStore(t *testing.T) {
	zero := 0
	negative := -1
	tooLargeQueueWait := 3600001
	tooLargeImageLane := 1000001
	invalidOverflow := "drop"
	tests := []struct {
		name  string
		input CreateInput
	}{
		{
			name:  "missing owner",
			input: validCreateInput(),
		},
		{
			name: "missing name",
			input: func() CreateInput {
				input := validCreateInput()
				input.Name = " "
				return input
			}(),
		},
		{
			name: "missing provider",
			input: func() CreateInput {
				input := validCreateInput()
				input.ProviderCode = " "
				return input
			}(),
		},
		{
			name: "invalid group type",
			input: func() CreateInput {
				input := validCreateInput()
				input.GroupType = "shared"
				return input
			}(),
		},
		{
			name: "group type is not trimmed",
			input: func() CreateInput {
				input := validCreateInput()
				input.GroupType = " personal "
				return input
			}(),
		},
		{
			name: "default soft concurrency below minimum",
			input: func() CreateInput {
				input := validCreateInput()
				input.SchedulingPolicy = &SchedulingPolicyInput{DefaultSoftConcurrency: &zero}
				return input
			}(),
		},
		{
			name: "queue wait above maximum",
			input: func() CreateInput {
				input := validCreateInput()
				input.SchedulingPolicy = &SchedulingPolicyInput{MaxQueueWaitMs: &tooLargeQueueWait}
				return input
			}(),
		},
		{
			name: "client ip concurrency below minimum",
			input: func() CreateInput {
				input := validCreateInput()
				input.SchedulingPolicy = &SchedulingPolicyInput{ClientIPConcurrencyLimit: &negative}
				return input
			}(),
		},
		{
			name: "invalid overflow mode",
			input: func() CreateInput {
				input := validCreateInput()
				input.SchedulingPolicy = &SchedulingPolicyInput{ClientIPConcurrencyOverflowMode: &invalidOverflow}
				return input
			}(),
		},
		{
			name: "overflow mode is not trimmed",
			input: func() CreateInput {
				input := validCreateInput()
				mode := " queue "
				input.SchedulingPolicy = &SchedulingPolicyInput{ClientIPConcurrencyOverflowMode: &mode}
				return input
			}(),
		},
		{
			name: "image lane above maximum",
			input: func() CreateInput {
				input := validCreateInput()
				input.SchedulingPolicy = &SchedulingPolicyInput{ImageLaneMaxConcurrency: &tooLargeImageLane}
				return input
			}(),
		},
	}
	tests[0].input.SystemAccountID = ""

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &groupOptionStoreStub{}
			service := NewService(store)

			_, err := service.Create(context.Background(), tt.input)
			if err == nil {
				t.Fatal("Create() error = nil, want validation error")
			}
			if _, ok := ValidationMessage(err); !ok {
				t.Fatalf("Create() error = %T %v, want ValidationError", err, err)
			}
			if store.createCalls != 0 {
				t.Fatalf("CreateManagementGroup() calls = %d, want 0", store.createCalls)
			}
		})
	}
}

func TestServiceCreateMapsStoreErrorsForHTTP(t *testing.T) {
	tests := []struct {
		name        string
		storeErr    error
		assertError func(*testing.T, error)
	}{
		{
			name:     "system account missing",
			storeErr: port.ErrManagementGroupSystemAccountNotFound,
			assertError: func(t *testing.T, err error) {
				if !errors.Is(err, ErrSystemAccountNotFound) {
					t.Fatalf("Create() error = %v, want ErrSystemAccountNotFound", err)
				}
			},
		},
		{
			name:     "provider missing",
			storeErr: port.ErrManagementGroupProviderNotFound,
			assertError: func(t *testing.T, err error) {
				message, ok := ProviderNotFoundMessage(err)
				if !ok || message != "不支持的供应商：openai" {
					t.Fatalf("provider not found error = %T %v", err, err)
				}
			},
		},
		{
			name:     "provider disabled",
			storeErr: port.ErrManagementGroupProviderDisabled,
			assertError: func(t *testing.T, err error) {
				message, ok := ProviderDisabledMessage(err)
				if !ok || message != "供应商已停用：openai" {
					t.Fatalf("provider disabled error = %T %v", err, err)
				}
			},
		},
		{
			name:     "name exists",
			storeErr: port.ErrManagementGroupNameExists,
			assertError: func(t *testing.T, err error) {
				message, ok := NameExistsMessage(err)
				if !ok || message != "同一供应商下分组名称已存在：测试分组" {
					t.Fatalf("name exists error = %T %v", err, err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &groupOptionStoreStub{createErr: tt.storeErr}
			service := NewService(store)

			_, err := service.Create(context.Background(), validCreateInput())
			if err == nil {
				t.Fatal("Create() error = nil")
			}
			tt.assertError(t, err)
		})
	}
}

func TestServiceCreateInvalidatesDetachedAndIgnoresFailure(t *testing.T) {
	store := &groupOptionStoreStub{}
	invalidator := &groupRuntimeInvalidatorStub{err: errors.New("redis unavailable")}
	var logs bytes.Buffer
	service := NewServiceWithOptions(ServiceOptions{
		Store:       store,
		Invalidator: invalidator,
		Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Create(requestCtx, validCreateInput())
	if err != nil {
		t.Fatalf("Create() error = %v, want nil despite invalidation failure", err)
	}
	if result.ID == "" {
		t.Fatalf("Create() result = %+v", result)
	}
	if invalidator.calls != 1 ||
		invalidator.reason != GroupCreatedReason ||
		invalidator.contextErr != nil {
		t.Fatalf("invalidator = %+v", invalidator)
	}
	if !strings.Contains(logs.String(), "management_group_gateway_runtime_invalidation_failed") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestServiceCreateStoreFailureSkipsInvalidation(t *testing.T) {
	wantErr := errors.New("postgres unavailable")
	store := &groupOptionStoreStub{createErr: wantErr}
	invalidator := &groupRuntimeInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})

	_, err := service.Create(context.Background(), validCreateInput())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Create() error = %v, want %v", err, wantErr)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
}

func validCreateInput() CreateInput {
	return CreateInput{
		SystemAccountID: "sys_owner",
		Name:            "测试分组",
		ProviderCode:    "openai",
	}
}

type groupOptionStoreStub struct {
	input          port.ManagementGroupOptionListInput
	accountInput   port.ManagementGroupOptionListInput
	options        []port.ManagementGroupOption
	accountOptions []port.ManagementGroupAccountOption
	err            error
	createInput    port.ManagementGroupCreateInput
	createResult   port.ManagementGroupSummary
	createErr      error
	createCalls    int
}

func (s *groupOptionStoreStub) ListManagementGroupOptions(_ context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error) {
	s.input = input
	return s.options, s.err
}

func (s *groupOptionStoreStub) ListManagementGroupAccountOptions(_ context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupAccountOption, error) {
	s.accountInput = input
	return s.accountOptions, s.err
}

func (s *groupOptionStoreStub) CreateManagementGroup(_ context.Context, input port.ManagementGroupCreateInput) (port.ManagementGroupSummary, error) {
	s.createCalls++
	s.createInput = input
	if s.createErr != nil {
		return port.ManagementGroupSummary{}, s.createErr
	}
	if s.createResult.ID != "" {
		return s.createResult, nil
	}
	return port.ManagementGroupSummary{
		ID:                   input.ID,
		SystemAccountID:      input.SystemAccountID,
		Name:                 input.Name,
		ProviderCode:         input.ProviderCode,
		Description:          input.Description,
		Enabled:              input.Enabled,
		IsDefault:            false,
		GroupType:            input.GroupType,
		SchedulingPolicyJSON: input.SchedulingPolicyJSON,
	}, nil
}

type groupRuntimeInvalidatorStub struct {
	calls      int
	reason     string
	contextErr error
	err        error
}

func (s *groupRuntimeInvalidatorStub) InvalidateGatewayRuntime(ctx context.Context, reason string) error {
	s.calls++
	s.reason = reason
	s.contextErr = ctx.Err()
	return s.err
}
