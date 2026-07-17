//go:build integration

package integration

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/modules/publicaccounts"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w1bInvalidSupportedModelsMessage  = "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型"
	w1bDuplicateAccountNameMessage    = "账号已存在：公开账号"
	w1bProfileDefaultHealthCheckModel = "gpt-5.6-sol"
	w1bValidBuiltInModel              = "gpt-5.4-mini"
	w1bGlobalCustomModel              = "w1b-global-model"
	w1bAdminPersonalCustomModel       = "w1b-admin-personal-model"
	w1bOtherPersonalCustomModel       = "w1b-other-personal-model"
	w1bDisabledCustomModel            = "w1b-disabled-model"
	w1bUnpricedCustomModel            = "w1b-unpriced-model"
	w1bUnknownModel                   = "w1b-unknown-model"
	w1bCredentialSecret               = "w1b-public-account-integration-secret"
)

var w1bGPTDefaultSupportedModels = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-image-2",
}

type w1bPublicAccountModelBinding struct {
	Model     string
	CreatedAt time.Time
}

type w1bPublicAccountRuntimeState struct {
	Status                      string
	Schedulable                 bool
	CooldownUntil               sql.NullTime
	LastErrorCode               sql.NullString
	LastErrorMessage            sql.NullString
	NextHealthCheckAt           sql.NullTime
	HealthCheckFailureCount     int
	LastHealthCheckStatusCode   sql.NullInt64
	LastHealthCheckErrorCode    sql.NullString
	LastHealthCheckErrorMessage sql.NullString
}

func TestW1bPublicAccountsPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	service := publicaccounts.NewService(publicaccounts.Options{
		Store:          store,
		Transactor:     store,
		ProviderModels: managementprovidermodels.NewService(store),
		Now:            func() time.Time { return now },
		NewID:          sequenceID("w1b_account"),
		Secret:         w1bCredentialSecret,
	})

	initialSecret := "sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	created, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetDisplayName:         "管理员",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "公开账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    initialSecret,
		AvailabilitySchedule: publicaccounts.NewJSONValue(map[string]any{
			"enabled": true,
			"mode":    "always",
		}, true),
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if created.Action != "created" || !created.Target.Created || created.Target.GroupCreated == nil || !*created.Target.GroupCreated || created.Account == nil {
		t.Fatalf("created account = %+v", created)
	}
	if created.Account.Status != publicaccounts.StatusPendingTest || created.Account.Schedulable {
		t.Fatalf("created account status/schedulable = %s/%v", created.Account.Status, created.Account.Schedulable)
	}
	assertW1bPublicAccountModelList(t, created.Account.SupportedModels, w1bGPTDefaultSupportedModels)
	accountID := created.Account.ID
	assertW1bPublicAccountStored(t, ctx, db, accountID, initialSecret, publicaccounts.StatusPendingTest, false)
	assertW1bPublicAccountLastErrorMessage(t, ctx, db, accountID, "账户已保存，等待后台激活检查")
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bProfileDefaultHealthCheckModel)
	assertW1bPublicAccountResponseHidesHealthCheckModel(t, created)

	other, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "other",
		TargetDisplayName:         "其他用户",
		TargetGroupName:           "其他分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "其他账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-other",
	})
	if err != nil {
		t.Fatalf("add other public account: %v", err)
	}
	setW1bProviderSystemHealthCheckModel(t, ctx, db, "gpt", "gpt-5.5", now)
	systemDefaultAccount, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "系统默认检查模型账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-system-default",
		SupportedModels:           publicaccounts.NewStringListValue([]string{"gpt-5.5"}, true),
	})
	if err != nil {
		t.Fatalf("add system default health check account: %v", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, systemDefaultAccount.Account.ID, "gpt-5.5")

	setW1bProviderHealthCheckModelPreference(t, ctx, db, created.Target.SystemAccountID, "gpt", w1bValidBuiltInModel, now)
	personalDefaultAccount, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "个人默认检查模型账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-personal-default",
		SupportedModels:           publicaccounts.NewStringListValue([]string{w1bValidBuiltInModel}, true),
	})
	if err != nil {
		t.Fatalf("add personal default health check account: %v", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, personalDefaultAccount.Account.ID, w1bValidBuiltInModel)

	setW1bSystemAccountRole(t, ctx, db, created.Target.SystemAccountID, "admin")
	adminRoleAccount, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "管理员系统默认检查模型账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-admin-system-default",
		SupportedModels:           publicaccounts.NewStringListValue([]string{"gpt-5.5"}, true),
	})
	if err != nil {
		t.Fatalf("add admin-role system default health check account: %v", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, adminRoleAccount.Account.ID, "gpt-5.5")
	setW1bSystemAccountRole(t, ctx, db, created.Target.SystemAccountID, "user")

	insertW1bPublicAccountProviderModelFixture(
		t,
		ctx,
		db,
		now,
		created.Target.SystemAccountID,
		other.Target.SystemAccountID,
	)

	catalogCases := []struct {
		name        string
		accountName string
		model       string
		wantSuccess bool
	}{
		{name: "built-in", accountName: "内置目录账号", model: w1bValidBuiltInModel, wantSuccess: true},
		{name: "global", accountName: "全局目录账号", model: w1bGlobalCustomModel, wantSuccess: true},
		{name: "owner-personal", accountName: "管理员个人目录账号", model: w1bAdminPersonalCustomModel, wantSuccess: true},
		{name: "other-owner-personal", accountName: "其他所有者个人目录账号", model: w1bOtherPersonalCustomModel},
		{name: "disabled", accountName: "停用目录账号", model: w1bDisabledCustomModel},
		{name: "unpriced", accountName: "不可计价目录账号", model: w1bUnpricedCustomModel},
		{name: "unknown", accountName: "未知目录账号", model: w1bUnknownModel},
	}
	for _, testCase := range catalogCases {
		t.Run("catalog_"+testCase.name, func(t *testing.T) {
			setW1bProviderHealthCheckModelPreference(t, ctx, db, created.Target.SystemAccountID, "gpt", testCase.model, now)
			result, err := service.Add(ctx, publicaccounts.AddInput{
				TargetUsername:            "admin",
				TargetGroupName:           "账号分组",
				ProviderCode:              "gpt",
				ProviderProtocolProfileID: "profile_gpt_openai_v1",
				Name:                      testCase.accountName,
				Type:                      publicaccounts.AccountTypeAPIKey,
				BaseURL:                   "https://api.openai.com/v1",
				APIKey:                    "sk-" + testCase.name,
				SupportedModels:           publicaccounts.NewStringListValue([]string{testCase.model}, true),
			})
			if testCase.wantSuccess {
				if err != nil {
					t.Fatalf("add catalog account: %v", err)
				}
				if result.Account == nil {
					t.Fatalf("catalog account result = %+v", result)
				}
				assertW1bPublicAccountModelList(t, result.Account.SupportedModels, []string{testCase.model})
				assertW1bPublicAccountNameCount(t, ctx, db, testCase.accountName, 1)
				assertW1bPublicAccountHealthCheckModel(t, ctx, db, result.Account.ID, testCase.model)
				assertW1bPublicAccountResponseHidesHealthCheckModel(t, result)
				return
			}
			if !errors.Is(err, publicaccounts.ErrInvalidSupportedModels) {
				t.Fatalf("add catalog account err = %v, want ErrInvalidSupportedModels", err)
			}
			assertW1bPublicAccountNameCount(t, ctx, db, testCase.accountName, 0)
		})
	}

	emptyModelsAccountName := "空模型账号"
	if _, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      emptyModelsAccountName,
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-empty-models",
		SupportedModels:           publicaccounts.NewStringListValue([]string{}, true),
	}); err == nil {
		t.Fatal("explicit empty supportedModels add error is nil")
	} else {
		if !errors.Is(err, publicaccounts.ErrInvalidSupportedModels) {
			t.Fatalf("explicit empty supportedModels add err = %v, want ErrInvalidSupportedModels", err)
		}
		want := publicaccounts.ErrInvalidSupportedModels.Error() + ": " + w1bInvalidSupportedModelsMessage
		if err.Error() != want {
			t.Fatalf("explicit empty supportedModels add err = %q, want %q", err.Error(), want)
		}
	}
	assertW1bPublicAccountNameCount(t, ctx, db, emptyModelsAccountName, 0)

	if _, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "公开账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-duplicate-empty-models",
		SupportedModels:           publicaccounts.NewStringListValue([]string{}, true),
	}); !errors.Is(err, publicaccounts.ErrDuplicateAccountName) {
		t.Fatalf("duplicate add with explicit empty supportedModels err = %v, want ErrDuplicateAccountName", err)
	}
	assertW1bPublicAccountNameCount(t, ctx, db, "公开账号", 1)

	if _, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "公开账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-duplicate-unknown",
		SupportedModels:           publicaccounts.NewStringListValue([]string{w1bUnknownModel}, true),
	}); !errors.Is(err, publicaccounts.ErrDuplicateAccountName) {
		t.Fatalf("duplicate add with unknown supportedModels err = %v, want ErrDuplicateAccountName", err)
	}
	assertW1bPublicAccountNameCount(t, ctx, db, "公开账号", 1)

	listed, err := service.List(ctx, publicaccounts.ListInput{
		TargetUsername:            "admin",
		TargetGroupName:           "账号分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Keyword:                   "公开",
		Status:                    "all",
		Page:                      1,
		PageSize:                  10,
	})
	if err != nil {
		t.Fatalf("list public accounts: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != accountID || listed.Items[0].ConcurrencyLimit == nil || *listed.Items[0].ConcurrencyLimit != publicaccounts.DefaultConcurrencyLimit {
		t.Fatalf("listed accounts = %+v", listed.Items)
	}

	active := publicaccounts.StatusActive
	if _, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID: accountID,
		Status:    &active,
	}); !errors.Is(err, publicaccounts.ErrInvalidStatusTransition) {
		t.Fatalf("pending -> active err = %v, want ErrInvalidStatusTransition", err)
	}

	credentialExtensions := map[string]any{
		"api_key":                  initialSecret,
		"base_url":                 "https://api.openai.com/v1",
		"service_tier":             "priority",
		"reasoning_effort":         "high",
		"supported_endpoint_modes": []any{"chat_json", "chat_sse", "responses_json", "responses_sse"},
		"endpoint": map[string]any{
			"path": "/responses",
			"mode": "strict",
		},
		"error": map[string]any{
			"code":   "fixture_error",
			"policy": "retry",
		},
	}
	setW1bPublicAccountCredentials(t, ctx, db, accountID, w1bCredentialSecret, credentialExtensions)
	seedW1bPublicAccountRuntimeState(t, ctx, db, accountID, now)
	partialUpdatedSecret := "sk-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	updatedCredentials, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID: accountID,
		APIKey:    &partialUpdatedSecret,
	})
	if err != nil {
		t.Fatalf("partially update public account credentials: %v", err)
	}
	if updatedCredentials.Account == nil ||
		updatedCredentials.Account.Status != publicaccounts.StatusPendingTest ||
		updatedCredentials.Account.Schedulable {
		t.Fatalf("partially updated account = %+v", updatedCredentials.Account)
	}
	credentialExtensions["api_key"] = partialUpdatedSecret
	assertW1bPublicAccountCredentials(
		t,
		ctx,
		db,
		accountID,
		w1bCredentialSecret,
		credentialExtensions,
	)
	assertW1bPublicAccountRuntimeReset(t, ctx, db, accountID)
	seedW1bPublicAccountPendingHealthDiagnostics(t, ctx, db, accountID, now)
	pendingNotes := "待检查状态下仅更新备注"
	if _, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID: accountID,
		Notes:     publicaccounts.NewOptionalString(&pendingNotes, true),
	}); err != nil {
		t.Fatalf("update pending public account notes: %v", err)
	}
	assertW1bPublicAccountRuntimeReset(t, ctx, db, accountID)

	invalidUpdatedName := "不应保存的账号名称"
	invalidUpdatedSecret := "sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:       accountID,
		Name:            &invalidUpdatedName,
		APIKey:          &invalidUpdatedSecret,
		SupportedModels: publicaccounts.NewStringListValue([]string{w1bUnknownModel}, true),
	}); !errors.Is(err, publicaccounts.ErrInvalidSupportedModels) {
		t.Fatalf("update with unknown supportedModels err = %v, want ErrInvalidSupportedModels", err)
	}
	assertW1bPublicAccountName(t, ctx, db, accountID, "公开账号")
	assertW1bPublicAccountStored(t, ctx, db, accountID, partialUpdatedSecret, publicaccounts.StatusPendingTest, false)
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)

	// Make any unintended delete-and-reinsert use a different created_at value.
	now = now.Add(time.Minute)
	seedW1bPublicAccountRuntimeState(t, ctx, db, accountID, now)
	runtimeBeforeOmittedUpdate := readW1bPublicAccountRuntimeState(t, ctx, db, accountID)
	modelBindingsBeforeOmittedUpdate := readW1bPublicAccountModelBindings(t, ctx, db, accountID)
	preservedWithoutModelUpdate, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID: accountID,
		Notes:     publicaccounts.NewOptionalString(ptrIntegrationString("仅更新备注"), true),
	})
	if err != nil {
		t.Fatalf("update public account without supportedModels: %v", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bProfileDefaultHealthCheckModel)
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)
	assertW1bPublicAccountModelBindingsUnchanged(t, ctx, db, accountID, modelBindingsBeforeOmittedUpdate)
	assertW1bPublicAccountResponseHidesHealthCheckModel(t, preservedWithoutModelUpdate)
	assertW1bPublicAccountRuntimeState(
		t,
		readW1bPublicAccountRuntimeState(t, ctx, db, accountID),
		runtimeBeforeOmittedUpdate,
	)

	setW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bUnknownModel)
	modelBindingsBeforeInvalidHealthUpdate := readW1bPublicAccountModelBindings(t, ctx, db, accountID)
	if _, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID: accountID,
		Notes:     publicaccounts.NewOptionalString(ptrIntegrationString("不应保存的备注"), true),
	}); !errors.Is(err, publicaccounts.ErrInvalidHealthCheckModel) {
		t.Fatalf("update public account with invalid health check model err = %v, want ErrInvalidHealthCheckModel", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bUnknownModel)
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)
	assertW1bPublicAccountModelBindingsUnchanged(t, ctx, db, accountID, modelBindingsBeforeInvalidHealthUpdate)

	setW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bProfileDefaultHealthCheckModel)
	equivalentModels := slices.Clone(w1bGPTDefaultSupportedModels)
	slices.Reverse(equivalentModels)
	equivalentModels = append(equivalentModels, " "+w1bValidBuiltInModel+" ")
	modelBindingsBeforeEquivalentUpdate := readW1bPublicAccountModelBindings(t, ctx, db, accountID)
	preservedForEquivalentModels, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:       accountID,
		SupportedModels: publicaccounts.NewStringListValue(equivalentModels, true),
	})
	if err != nil {
		t.Fatalf("update public account with equivalent supportedModels: %v", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bProfileDefaultHealthCheckModel)
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)
	assertW1bPublicAccountModelBindingsUnchanged(t, ctx, db, accountID, modelBindingsBeforeEquivalentUpdate)
	assertW1bPublicAccountResponseHidesHealthCheckModel(t, preservedForEquivalentModels)
	assertW1bPublicAccountRuntimeReset(t, ctx, db, accountID)

	setW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bValidBuiltInModel)
	seedW1bPublicAccountRuntimeState(t, ctx, db, accountID, now)
	preservedForContainingModels, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:       accountID,
		SupportedModels: publicaccounts.NewStringListValue([]string{w1bValidBuiltInModel, "gpt-5.5"}, true),
	})
	if err != nil {
		t.Fatalf("update public account with containing supportedModels: %v", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bValidBuiltInModel)
	assertW1bPublicAccountModels(t, ctx, db, accountID, []string{w1bValidBuiltInModel, "gpt-5.5"})
	assertW1bPublicAccountResponseHidesHealthCheckModel(t, preservedForContainingModels)
	assertW1bPublicAccountRuntimeReset(t, ctx, db, accountID)

	modelBindingsBeforeRejectedRemoval := readW1bPublicAccountModelBindings(t, ctx, db, accountID)
	if _, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:       accountID,
		SupportedModels: publicaccounts.NewStringListValue([]string{"gpt-5.5"}, true),
	}); !errors.Is(err, publicaccounts.ErrInvalidHealthCheckModel) {
		t.Fatalf("update public account excluding health check model err = %v, want ErrInvalidHealthCheckModel", err)
	}
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, accountID, w1bValidBuiltInModel)
	assertW1bPublicAccountModels(t, ctx, db, accountID, []string{w1bValidBuiltInModel, "gpt-5.5"})
	assertW1bPublicAccountModelBindingsUnchanged(t, ctx, db, accountID, modelBindingsBeforeRejectedRemoval)

	seedW1bPublicAccountRuntimeState(t, ctx, db, accountID, now)
	runtimeBeforeDisabledUpdate := readW1bPublicAccountRuntimeState(t, ctx, db, accountID)
	updatedSecret := "sk-fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	updatedBaseURL := "https://api.openai.com/v2"
	updatedName := "公开账号更新"
	disabled := publicaccounts.StatusDisabled
	concurrencyLimit := 7
	priority := 3
	notes := "更新后的备注"
	updated, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:                 accountID,
		TargetUsername:            ptrIntegrationString("admin"),
		TargetGroupName:           ptrIntegrationString("账号分组"),
		ProviderCode:              ptrIntegrationString("gpt"),
		ProviderProtocolProfileID: ptrIntegrationString("profile_gpt_openai_v1"),
		Name:                      &updatedName,
		Status:                    &disabled,
		BaseURL:                   &updatedBaseURL,
		APIKey:                    &updatedSecret,
		SupportedModels:           publicaccounts.NewStringListValue([]string{w1bValidBuiltInModel}, true),
		ConcurrencyLimit:          &concurrencyLimit,
		Priority:                  &priority,
		Notes:                     publicaccounts.NewOptionalString(&notes, true),
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if updated.Account == nil || updated.Account.Name != updatedName || updated.Account.Status != publicaccounts.StatusDisabled || updated.Account.Schedulable {
		t.Fatalf("updated account = %+v", updated.Account)
	}
	assertW1bPublicAccountStored(t, ctx, db, accountID, updatedSecret, publicaccounts.StatusDisabled, false)
	assertW1bPublicAccountModels(t, ctx, db, accountID, []string{w1bValidBuiltInModel})
	assertW1bPublicAccountDisabledRuntimeState(
		t,
		readW1bPublicAccountRuntimeState(t, ctx, db, accountID),
		runtimeBeforeDisabledUpdate,
	)

	hybridModel := "w1b-hybrid-arbitrary-model"
	setW1bProviderHealthCheckModelPreference(t, ctx, db, created.Target.SystemAccountID, "hybrid", hybridModel, now)
	hybrid, err := service.Add(ctx, publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "混合账号分组",
		ProviderCode:              "hybrid",
		ProviderProtocolProfileID: "profile_hybrid_openai_chat_v1",
		Name:                      "混合供应商账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://hybrid.example.com/v1",
		APIKey:                    "sk-hybrid",
		SupportedModels:           publicaccounts.NewStringListValue([]string{hybridModel}, true),
	})
	if err != nil {
		t.Fatalf("add hybrid public account with arbitrary model: %v", err)
	}
	if hybrid.Account == nil {
		t.Fatalf("hybrid account result = %+v", hybrid)
	}
	assertW1bPublicAccountModelList(t, hybrid.Account.SupportedModels, []string{hybridModel})
	assertW1bPublicAccountModels(t, ctx, db, hybrid.Account.ID, []string{hybridModel})
	assertW1bPublicAccountHealthCheckModel(t, ctx, db, hybrid.Account.ID, hybridModel)

	wrongTarget := other.Target.Username
	notFound, err := service.Delete(ctx, publicaccounts.DeleteInput{AccountID: accountID, TargetUsername: &wrongTarget})
	if err != nil {
		t.Fatalf("delete with wrong target returned err: %v", err)
	}
	if notFound.Action != "not_found" || notFound.Account != nil {
		t.Fatalf("wrong-target delete = %+v", notFound)
	}

	deleted, err := service.Delete(ctx, publicaccounts.DeleteInput{
		AccountID:                 accountID,
		TargetUsername:            ptrIntegrationString("admin"),
		ProviderCode:              ptrIntegrationString("gpt"),
		ProviderProtocolProfileID: ptrIntegrationString("profile_gpt_openai_v1"),
	})
	if err != nil {
		t.Fatalf("delete public account: %v", err)
	}
	if deleted.Action != "deleted" || deleted.Account == nil || deleted.Account.ID != accountID {
		t.Fatalf("deleted account = %+v", deleted)
	}
	assertW1bPublicAccountSoftDeleted(t, ctx, db, accountID)
}

func insertW1bPublicAccountProviderModelFixture(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	adminSystemAccountID string,
	otherSystemAccountID string,
) {
	t.Helper()

	price := 1.0
	fixtures := []struct {
		id              string
		model           string
		scope           string
		systemAccountID *string
		status          string
		price           *float64
		createdBy       string
	}{
		{id: "custom_model_w1b_global", model: w1bGlobalCustomModel, scope: "global", status: "active", price: &price, createdBy: adminSystemAccountID},
		{id: "custom_model_w1b_admin_personal", model: w1bAdminPersonalCustomModel, scope: "personal", systemAccountID: &adminSystemAccountID, status: "active", price: &price, createdBy: adminSystemAccountID},
		{id: "custom_model_w1b_other_personal", model: w1bOtherPersonalCustomModel, scope: "personal", systemAccountID: &otherSystemAccountID, status: "active", price: &price, createdBy: otherSystemAccountID},
		{id: "custom_model_w1b_disabled", model: w1bDisabledCustomModel, scope: "personal", systemAccountID: &adminSystemAccountID, status: "disabled", price: &price, createdBy: adminSystemAccountID},
		{id: "custom_model_w1b_unpriced", model: w1bUnpricedCustomModel, scope: "personal", systemAccountID: &adminSystemAccountID, status: "active", createdBy: adminSystemAccountID},
	}
	for _, fixture := range fixtures {
		_, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.custom_provider_models (
				id, provider_code, model, scope, system_account_id, status, mode,
				supported_api_protocols_json, input_usd_per_1m, currency,
				created_by, updated_by, created_at, updated_at
			) VALUES (
				$1, 'gpt', $2, $3, $4, $5, 'chat',
				'["chat_completions","responses"]', $6, 'USD',
				$7, $7, $8, $8
			)
		`, fixture.id, fixture.model, fixture.scope, fixture.systemAccountID, fixture.status, fixture.price, fixture.createdBy, now)
		if err != nil {
			t.Fatalf("insert public account provider model fixture %s: %v", fixture.id, err)
		}
	}
}

func assertW1bPublicAccountStored(t *testing.T, ctx context.Context, db *sql.DB, id string, secret string, wantStatus string, wantSchedulable bool) {
	t.Helper()

	var encrypted string
	var fingerprint sql.NullString
	var mask string
	var status string
	var schedulable bool
	err := db.QueryRowContext(ctx, `
		SELECT credentials_encrypted, credential_fingerprint, credential_mask, status, schedulable
		FROM juhe_business.accounts
		WHERE id = $1 AND deleted_at IS NULL
	`, id).Scan(&encrypted, &fingerprint, &mask, &status, &schedulable)
	if err != nil {
		t.Fatalf("read public account: %v", err)
	}
	sum := sha256.Sum256([]byte(secret))
	if !fingerprint.Valid || fingerprint.String != hex.EncodeToString(sum[:]) {
		t.Fatalf("credential fingerprint = %q valid=%v", fingerprint.String, fingerprint.Valid)
	}
	if !strings.HasPrefix(encrypted, "v1:") || strings.Contains(encrypted, secret) || strings.Contains(encrypted, "api.openai.com") {
		t.Fatalf("credentials_encrypted is not encrypted enough: %q", encrypted)
	}
	if mask == "" || mask == secret || strings.Contains(mask, secret) {
		t.Fatalf("credential_mask = %q", mask)
	}
	if status != wantStatus || schedulable != wantSchedulable {
		t.Fatalf("status/schedulable = %s/%v, want %s/%v", status, schedulable, wantStatus, wantSchedulable)
	}
}

func setW1bPublicAccountCredentials(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	accountID string,
	secret string,
	credentials map[string]any,
) {
	t.Helper()

	encrypted := encryptW1bCredentials(t, secret, credentials)
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET credentials_encrypted = $2
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID, encrypted); err != nil {
		t.Fatalf("set public account credentials: %v", err)
	}
}

func assertW1bPublicAccountCredentials(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	accountID string,
	secret string,
	want map[string]any,
) {
	t.Helper()

	var encrypted string
	if err := db.QueryRowContext(ctx, `
		SELECT credentials_encrypted
		FROM juhe_business.accounts
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID).Scan(&encrypted); err != nil {
		t.Fatalf("read public account credentials: %v", err)
	}
	got := decryptW1bCredentials(t, secret, encrypted)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credentials = %#v, want %#v", got, want)
	}
}

func encryptW1bCredentials(t *testing.T, secret string, credentials map[string]any) string {
	t.Helper()

	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("create credential cipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("create credential GCM: %v", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("create credential nonce: %v", err)
	}
	plain, err := json.Marshal(credentials)
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	sealed := aead.Seal(nil, nonce, plain, nil)
	tagSize := aead.Overhead()
	encode := base64.RawURLEncoding.EncodeToString
	return "v1:" +
		encode(nonce) + ":" +
		encode(sealed[len(sealed)-tagSize:]) + ":" +
		encode(sealed[:len(sealed)-tagSize])
}

func decryptW1bCredentials(t *testing.T, secret string, encrypted string) map[string]any {
	t.Helper()

	parts := strings.Split(encrypted, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("credential format = %q", encrypted)
	}
	decode := base64.RawURLEncoding.DecodeString
	nonce, err := decode(parts[1])
	if err != nil {
		t.Fatalf("decode credential nonce: %v", err)
	}
	tag, err := decode(parts[2])
	if err != nil {
		t.Fatalf("decode credential tag: %v", err)
	}
	ciphertext, err := decode(parts[3])
	if err != nil {
		t.Fatalf("decode credential ciphertext: %v", err)
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("create credential cipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("create credential GCM: %v", err)
	}
	plain, err := aead.Open(nil, nonce, append(ciphertext, tag...), nil)
	if err != nil {
		t.Fatalf("decrypt credentials: %v", err)
	}
	var credentials map[string]any
	if err := json.Unmarshal(plain, &credentials); err != nil {
		t.Fatalf("unmarshal credentials: %v", err)
	}
	return credentials
}

func seedW1bPublicAccountRuntimeState(t *testing.T, ctx context.Context, db *sql.DB, accountID string, now time.Time) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET status = 'active',
		    schedulable = true,
		    cooldown_until = $2,
		    last_error_code = 'fixture_runtime_error',
		    last_error_message = 'fixture runtime error',
		    next_health_check_at = $3,
		    health_check_failure_count = 3,
		    last_health_check_status_code = 503,
		    last_health_check_error_code = 'fixture_health_error',
		    last_health_check_error_message = 'fixture health error'
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID, now.Add(30*time.Minute), now.Add(10*time.Minute)); err != nil {
		t.Fatalf("seed public account runtime state: %v", err)
	}
}

func seedW1bPublicAccountPendingHealthDiagnostics(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	accountID string,
	now time.Time,
) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET status = 'pending_test',
		    schedulable = false,
		    next_health_check_at = $2,
		    health_check_failure_count = 3,
		    last_health_check_status_code = 503,
		    last_health_check_error_code = 'fixture_health_error',
		    last_health_check_error_message = 'fixture health error'
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("seed pending public account health diagnostics: %v", err)
	}
}

func readW1bPublicAccountRuntimeState(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	accountID string,
) w1bPublicAccountRuntimeState {
	t.Helper()

	var state w1bPublicAccountRuntimeState
	if err := db.QueryRowContext(ctx, `
		SELECT
		  status,
		  schedulable,
		  cooldown_until,
		  last_error_code,
		  last_error_message,
		  next_health_check_at,
		  health_check_failure_count,
		  last_health_check_status_code,
		  last_health_check_error_code,
		  last_health_check_error_message
		FROM juhe_business.accounts
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID).Scan(
		&state.Status,
		&state.Schedulable,
		&state.CooldownUntil,
		&state.LastErrorCode,
		&state.LastErrorMessage,
		&state.NextHealthCheckAt,
		&state.HealthCheckFailureCount,
		&state.LastHealthCheckStatusCode,
		&state.LastHealthCheckErrorCode,
		&state.LastHealthCheckErrorMessage,
	); err != nil {
		t.Fatalf("read public account runtime state: %v", err)
	}
	return state
}

func assertW1bPublicAccountRuntimeReset(t *testing.T, ctx context.Context, db *sql.DB, accountID string) {
	t.Helper()

	got := readW1bPublicAccountRuntimeState(t, ctx, db, accountID)
	if got.Status != publicaccounts.StatusPendingTest ||
		got.Schedulable ||
		got.CooldownUntil.Valid ||
		got.LastErrorCode.Valid ||
		!got.LastErrorMessage.Valid ||
		got.LastErrorMessage.String != "账户配置已保存，等待后台检查" ||
		got.NextHealthCheckAt.Valid ||
		got.HealthCheckFailureCount != 0 ||
		got.LastHealthCheckStatusCode.Valid ||
		got.LastHealthCheckErrorCode.Valid ||
		got.LastHealthCheckErrorMessage.Valid {
		t.Fatalf("runtime state was not reset: %+v", got)
	}
}

func assertW1bPublicAccountRuntimeState(
	t *testing.T,
	got w1bPublicAccountRuntimeState,
	want w1bPublicAccountRuntimeState,
) {
	t.Helper()

	if got.Status != want.Status ||
		got.Schedulable != want.Schedulable ||
		!equalW1bNullTime(got.CooldownUntil, want.CooldownUntil) ||
		got.LastErrorCode != want.LastErrorCode ||
		got.LastErrorMessage != want.LastErrorMessage ||
		!equalW1bNullTime(got.NextHealthCheckAt, want.NextHealthCheckAt) ||
		got.HealthCheckFailureCount != want.HealthCheckFailureCount ||
		got.LastHealthCheckStatusCode != want.LastHealthCheckStatusCode ||
		got.LastHealthCheckErrorCode != want.LastHealthCheckErrorCode ||
		got.LastHealthCheckErrorMessage != want.LastHealthCheckErrorMessage {
		t.Fatalf("runtime state = %+v, want %+v", got, want)
	}
}

func assertW1bPublicAccountDisabledRuntimeState(
	t *testing.T,
	got w1bPublicAccountRuntimeState,
	before w1bPublicAccountRuntimeState,
) {
	t.Helper()

	if got.Status != publicaccounts.StatusDisabled ||
		got.Schedulable ||
		got.CooldownUntil.Valid ||
		got.LastErrorCode.Valid ||
		got.LastErrorMessage.Valid ||
		!equalW1bNullTime(got.NextHealthCheckAt, before.NextHealthCheckAt) ||
		got.HealthCheckFailureCount != before.HealthCheckFailureCount ||
		got.LastHealthCheckStatusCode != before.LastHealthCheckStatusCode ||
		got.LastHealthCheckErrorCode != before.LastHealthCheckErrorCode ||
		got.LastHealthCheckErrorMessage != before.LastHealthCheckErrorMessage {
		t.Fatalf("disabled runtime state = %+v, before %+v", got, before)
	}
}

func equalW1bNullTime(left sql.NullTime, right sql.NullTime) bool {
	if left.Valid != right.Valid {
		return false
	}
	return !left.Valid || left.Time.Equal(right.Time)
}

func assertW1bPublicAccountName(t *testing.T, ctx context.Context, db *sql.DB, accountID string, want string) {
	t.Helper()

	var got string
	if err := db.QueryRowContext(ctx, `
		SELECT name
		FROM juhe_business.accounts
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID).Scan(&got); err != nil {
		t.Fatalf("read public account name: %v", err)
	}
	if got != want {
		t.Fatalf("public account name = %q, want %q", got, want)
	}
}

func assertW1bPublicAccountLastErrorMessage(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	accountID string,
	want string,
) {
	t.Helper()

	var got sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT last_error_message
		FROM juhe_business.accounts
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID).Scan(&got); err != nil {
		t.Fatalf("read public account last error message: %v", err)
	}
	if !got.Valid || got.String != want {
		t.Fatalf("last_error_message = %q valid=%v, want %q", got.String, got.Valid, want)
	}
}

func setW1bProviderHealthCheckModelPreference(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	systemAccountID string,
	providerCode string,
	model string,
	now time.Time,
) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.provider_default_health_check_models (
			system_account_id, provider_code, model, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (system_account_id, provider_code) DO UPDATE
		SET model = EXCLUDED.model,
		    updated_at = EXCLUDED.updated_at
	`, systemAccountID, providerCode, model, now); err != nil {
		t.Fatalf("set provider health check model preference: %v", err)
	}
}

func setW1bProviderSystemHealthCheckModel(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	providerCode string,
	model string,
	now time.Time,
) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.provider_system_default_health_check_models (
			provider_code, model, created_at, updated_at
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider_code) DO UPDATE SET
			model = EXCLUDED.model,
			updated_at = EXCLUDED.updated_at
	`, providerCode, model, now, now)
	if err != nil {
		t.Fatalf("set provider system health check model: %v", err)
	}
}

func setW1bSystemAccountRole(t *testing.T, ctx context.Context, db *sql.DB, systemAccountID string, role string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.system_accounts
		SET role = $2, updated_at = now()
		WHERE id = $1
	`, systemAccountID, role); err != nil {
		t.Fatalf("set system account role %s: %v", role, err)
	}
}

func setW1bPublicAccountHealthCheckModel(t *testing.T, ctx context.Context, db *sql.DB, accountID string, model string) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET health_check_model = $2
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID, model); err != nil {
		t.Fatalf("set public account health check model: %v", err)
	}
}

func assertW1bPublicAccountHealthCheckModel(t *testing.T, ctx context.Context, db *sql.DB, accountID string, want string) {
	t.Helper()

	var got string
	if err := db.QueryRowContext(ctx, `
		SELECT health_check_model
		FROM juhe_business.accounts
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID).Scan(&got); err != nil {
		t.Fatalf("read public account health check model: %v", err)
	}
	if got != want {
		t.Fatalf("health_check_model = %q, want %q", got, want)
	}
}

func assertW1bPublicAccountResponseHidesHealthCheckModel(t *testing.T, response publicaccounts.AccountResponse) {
	t.Helper()

	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal public account response: %v", err)
	}
	if strings.Contains(string(payload), "healthCheckModel") || strings.Contains(string(payload), "health_check_model") {
		t.Fatalf("public account response exposed health check model: %s", payload)
	}
}

func readW1bPublicAccountModelBindings(t *testing.T, ctx context.Context, db *sql.DB, accountID string) []w1bPublicAccountModelBinding {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT model, created_at
		FROM juhe_business.account_supported_models
		WHERE account_id = $1
		ORDER BY model ASC
	`, accountID)
	if err != nil {
		t.Fatalf("query public account model bindings: %v", err)
	}
	defer rows.Close()

	var bindings []w1bPublicAccountModelBinding
	for rows.Next() {
		var binding w1bPublicAccountModelBinding
		if err := rows.Scan(&binding.Model, &binding.CreatedAt); err != nil {
			t.Fatalf("scan public account model binding: %v", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public account model bindings: %v", err)
	}
	return bindings
}

func assertW1bPublicAccountModelBindingsUnchanged(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	accountID string,
	before []w1bPublicAccountModelBinding,
) {
	t.Helper()

	after := readW1bPublicAccountModelBindings(t, ctx, db, accountID)
	if len(after) != len(before) {
		t.Fatalf("public account model binding count = %d, want %d; before=%+v after=%+v", len(after), len(before), before, after)
	}
	for i := range before {
		if after[i].Model != before[i].Model || !after[i].CreatedAt.Equal(before[i].CreatedAt) {
			t.Fatalf("public account model bindings changed; before=%+v after=%+v", before, after)
		}
	}
}

func assertW1bPublicAccountModels(t *testing.T, ctx context.Context, db *sql.DB, accountID string, want []string) {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT model
		FROM juhe_business.account_supported_models
		WHERE account_id = $1
		ORDER BY model ASC
	`, accountID)
	if err != nil {
		t.Fatalf("query public account models: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			t.Fatalf("scan public account model: %v", err)
		}
		got = append(got, model)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public account models: %v", err)
	}
	sortedWant := slices.Clone(want)
	slices.Sort(sortedWant)
	if !slices.Equal(got, sortedWant) {
		t.Fatalf("models = %v, want %v", got, sortedWant)
	}
}

func assertW1bPublicAccountModelList(t *testing.T, got []string, want []string) {
	t.Helper()

	sortedWant := slices.Clone(want)
	slices.Sort(sortedWant)
	if !slices.Equal(got, sortedWant) {
		t.Fatalf("supported models = %v, want %v", got, sortedWant)
	}
}

func assertW1bPublicAccountNameCount(t *testing.T, ctx context.Context, db *sql.DB, name string, want int) {
	t.Helper()

	var got int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)::int
		FROM juhe_business.accounts
		WHERE name = $1
	`, name).Scan(&got); err != nil {
		t.Fatalf("count public accounts named %q: %v", name, err)
	}
	if got != want {
		t.Fatalf("public accounts named %q = %d, want %d", name, got, want)
	}
}

func assertW1bPublicAccountSoftDeleted(t *testing.T, ctx context.Context, db *sql.DB, accountID string) {
	t.Helper()

	var deleted bool
	var bindingCount int
	err := db.QueryRowContext(ctx, `
		SELECT deleted_at IS NOT NULL
		FROM juhe_business.accounts
		WHERE id = $1
	`, accountID).Scan(&deleted)
	if err != nil {
		t.Fatalf("read deleted public account: %v", err)
	}
	if !deleted {
		t.Fatalf("account %s was not soft deleted", accountID)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)::int
		FROM juhe_business.group_accounts
		WHERE account_id = $1
	`, accountID).Scan(&bindingCount); err != nil {
		t.Fatalf("count public account bindings: %v", err)
	}
	if bindingCount != 0 {
		t.Fatalf("group account bindings = %d, want 0", bindingCount)
	}
}
