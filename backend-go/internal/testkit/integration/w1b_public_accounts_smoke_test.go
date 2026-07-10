//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/publicaccounts"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w1bInvalidSupportedModelsMessage = "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型"
	w1bDuplicateAccountNameMessage   = "账号已存在：公开账号"
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
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID:      sequenceID("w1b_account"),
		Secret:     "w1b-public-account-integration-secret",
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
		APIKey:                    "sk-duplicate",
	}); !errors.Is(err, publicaccounts.ErrDuplicateAccountName) {
		t.Fatalf("duplicate add err = %v, want ErrDuplicateAccountName", err)
	}

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
		SupportedModels:           publicaccounts.NewStringListValue([]string{"gpt-5.5-codex"}, true),
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
	assertW1bPublicAccountModels(t, ctx, db, accountID, []string{"gpt-5.5-codex"})

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
