//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	w1bInvalidSupportedModelsMessage = "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型"
	w1bDuplicateAccountNameMessage   = "账号已存在：公开账号"
	w1bValidBuiltInModel             = "gpt-5.4-mini"
	w1bGlobalCustomModel             = "w1b-global-model"
	w1bAdminPersonalCustomModel      = "w1b-admin-personal-model"
	w1bOtherPersonalCustomModel      = "w1b-other-personal-model"
	w1bDisabledCustomModel           = "w1b-disabled-model"
	w1bUnpricedCustomModel           = "w1b-unpriced-model"
	w1bUnknownModel                  = "w1b-unknown-model"
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
		Secret:         "w1b-public-account-integration-secret",
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
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)

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
	assertW1bPublicAccountStored(t, ctx, db, accountID, initialSecret, publicaccounts.StatusPendingTest, false)
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)

	setW1bPublicAccountDefaultTestModel(t, ctx, db, accountID, w1bUnknownModel)
	preservedWithoutModelUpdate, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID: accountID,
		Notes:     publicaccounts.NewOptionalString(ptrIntegrationString("仅更新备注"), true),
	})
	if err != nil {
		t.Fatalf("update public account without supportedModels: %v", err)
	}
	assertW1bPublicAccountDefaultTestModel(t, ctx, db, accountID, w1bUnknownModel)
	assertW1bPublicAccountResponseHidesDefaultTestModel(t, preservedWithoutModelUpdate)

	equivalentModels := slices.Clone(w1bGPTDefaultSupportedModels)
	slices.Reverse(equivalentModels)
	preservedForEquivalentModels, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:       accountID,
		SupportedModels: publicaccounts.NewStringListValue(equivalentModels, true),
	})
	if err != nil {
		t.Fatalf("update public account with equivalent supportedModels: %v", err)
	}
	assertW1bPublicAccountDefaultTestModel(t, ctx, db, accountID, w1bUnknownModel)
	assertW1bPublicAccountModels(t, ctx, db, accountID, w1bGPTDefaultSupportedModels)
	assertW1bPublicAccountResponseHidesDefaultTestModel(t, preservedForEquivalentModels)

	setW1bPublicAccountDefaultTestModel(t, ctx, db, accountID, w1bValidBuiltInModel)
	preservedForContainingModels, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:       accountID,
		SupportedModels: publicaccounts.NewStringListValue([]string{w1bValidBuiltInModel, "gpt-5.5"}, true),
	})
	if err != nil {
		t.Fatalf("update public account with containing supportedModels: %v", err)
	}
	assertW1bPublicAccountDefaultTestModel(t, ctx, db, accountID, w1bValidBuiltInModel)
	assertW1bPublicAccountModels(t, ctx, db, accountID, []string{w1bValidBuiltInModel, "gpt-5.5"})
	assertW1bPublicAccountResponseHidesDefaultTestModel(t, preservedForContainingModels)

	clearedForExcludingModels, err := service.Update(ctx, publicaccounts.UpdateInput{
		AccountID:       accountID,
		SupportedModels: publicaccounts.NewStringListValue([]string{"gpt-5.5"}, true),
	})
	if err != nil {
		t.Fatalf("update public account with excluding supportedModels: %v", err)
	}
	assertW1bPublicAccountDefaultTestModel(t, ctx, db, accountID, "")
	assertW1bPublicAccountModels(t, ctx, db, accountID, []string{"gpt-5.5"})
	assertW1bPublicAccountResponseHidesDefaultTestModel(t, clearedForExcludingModels)

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

	hybridModel := "w1b-hybrid-arbitrary-model"
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

func setW1bPublicAccountDefaultTestModel(t *testing.T, ctx context.Context, db *sql.DB, accountID string, model string) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET default_test_model = $2
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID, model); err != nil {
		t.Fatalf("set public account default test model: %v", err)
	}
}

func assertW1bPublicAccountDefaultTestModel(t *testing.T, ctx context.Context, db *sql.DB, accountID string, want string) {
	t.Helper()

	var got sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT default_test_model
		FROM juhe_business.accounts
		WHERE id = $1 AND deleted_at IS NULL
	`, accountID).Scan(&got); err != nil {
		t.Fatalf("read public account default test model: %v", err)
	}
	if want == "" {
		if got.Valid {
			t.Fatalf("default_test_model = %q, want NULL", got.String)
		}
		return
	}
	if !got.Valid || got.String != want {
		t.Fatalf("default_test_model = %q valid=%v, want %q", got.String, got.Valid, want)
	}
}

func assertW1bPublicAccountResponseHidesDefaultTestModel(t *testing.T, response publicaccounts.AccountResponse) {
	t.Helper()

	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal public account response: %v", err)
	}
	if strings.Contains(string(payload), "defaultTestModel") || strings.Contains(string(payload), "default_test_model") {
		t.Fatalf("public account response exposed default test model: %s", payload)
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

	if !slices.Equal(got, want) {
		t.Fatalf("supported models = %v, want %v", got, want)
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
