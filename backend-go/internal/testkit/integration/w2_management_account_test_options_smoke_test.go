//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementaccounttestoptions"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2AccountTestOptionsAdminToken   = "w2-account-test-options-admin-session"
	w2AccountTestOptionsOwnerToken   = "w2-account-test-options-owner-session"
	w2AccountTestOptionsGranteeToken = "w2-account-test-options-grantee-session"
	w2AccountTestOptionsSecret       = "w2-account-test-options-credential-secret"
	w2AccountTestOptionsCorruptText  = "w2-corrupt-ciphertext-do-not-leak"
)

func TestW2ManagementAccountTestOptionsPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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

	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	sessionLastSeenAt := now.Add(-30 * time.Minute)
	codec := secretcrypto.NewJSONCodec(w2AccountTestOptionsSecret)
	// Fixtures are written through SQL against the fresh Goose schema. This smoke
	// validates the real Go read stack, not Node-writer cross-runtime compatibility.
	insertW2AccountTestOptionsFixtures(t, ctx, db, codec, now, sessionLastSeenAt)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	providerModels := managementprovidermodels.NewService(store)
	service := managementaccounttestoptions.NewServiceWithOptions(managementaccounttestoptions.ServiceOptions{
		Reader:          store,
		OptionReader:    store,
		ModelCatalog:    providerModels,
		CredentialCodec: codec,
	})
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                                slog.Default(),
		ManagementAPIAuthMiddleware:           httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:      httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAccountTestOptionsHandler:   httpapi.NewManagementAccountTestOptionsHandler(service),
		ManagementMyAccountTestOptionsHandler: httpapi.NewManagementMyAccountTestOptionsHandler(service),
	})

	t.Run("authorization and admin scope", func(t *testing.T) {
		missingSession := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/accounts/acct_w2_test_hybrid/test-options",
			"",
		)
		assertW2AccountTestOptionsMessage(t, missingSession, http.StatusUnauthorized, "请先登录")

		ordinaryUser := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/accounts/acct_w2_test_hybrid/test-options",
			w2AccountTestOptionsOwnerToken,
		)
		assertW2AccountTestOptionsMessage(t, ordinaryUser, http.StatusForbidden, "需要管理员权限")

		globalPaths := []string{
			"/__aisys__/api/accounts/acct_w2_test_hybrid/test-options",
			"/__aisys__/api/accounts/acct_w2_test_hybrid/test-options?systemAccountId=",
			"/__aisys__/api/accounts/acct_w2_test_hybrid/test-options?systemAccountId=all",
			"/__aisys__/api/accounts/acct_w2_test_hybrid/test-options?systemAccountId=sys_w2_test_options_owner",
		}
		for _, path := range globalPaths {
			rec := requestW2AccountTestOptions(t, router, path, w2AccountTestOptionsAdminToken)
			assertW2AccountTestSelectionContains(t, decodeW2AccountTestSelectionOptions(t, rec, http.StatusOK), "w2-hybrid-owner-model")
		}

		wrongOwner := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/accounts/acct_w2_test_hybrid/test-options?systemAccountId=sys_w2_test_options_grantee",
			w2AccountTestOptionsAdminToken,
		)
		assertW2AccountTestOptionsMessage(t, wrongOwner, http.StatusNotFound, "账户不存在")

		globalAuthorized := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/accounts/acct_w2_test_authorized/test-options",
			w2AccountTestOptionsAdminToken,
		)
		assertW2AccountTestSelectionContains(t, decodeW2AccountTestSelectionOptions(t, globalAuthorized, http.StatusOK), "w2-source-owner-model")

		wrongAuthorizedViewer := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/accounts/acct_w2_test_authorized/test-options?systemAccountId=sys_w2_test_options_owner",
			w2AccountTestOptionsAdminToken,
		)
		assertW2AccountTestOptionsMessage(t, wrongAuthorizedViewer, http.StatusNotFound, "账户不存在")
	})

	t.Run("owner endpoint mode branches", func(t *testing.T) {
		testCases := []struct {
			name         string
			accountID    string
			defaultModel string
			wantModes    []string
		}{
			{
				name:         "hybrid intersects the default model with account modes and mappings",
				accountID:    "acct_w2_test_hybrid",
				defaultModel: "w2-hybrid-owner-model",
				wantModes:    []string{"messages_sse"},
			},
			{
				name:         "anthropic defaults exclude token counting",
				accountID:    "acct_w2_test_anthropic",
				defaultModel: "w2-anthropic-owner-model",
				wantModes:    []string{"messages_sse", "messages_json"},
			},
			{
				name:         "gemini disabled health mode falls back to enabled generation mode",
				accountID:    "acct_w2_test_gemini",
				defaultModel: "w2-gemini-owner-model",
				wantModes:    []string{"generate_content_json"},
			},
			{
				name:         "OpenAI OAuth defaults to responses generation modes",
				accountID:    "acct_w2_test_gpt_oauth",
				defaultModel: "w2-gpt-oauth-model",
				wantModes:    []string{"responses_sse", "responses_json"},
			},
		}
		for _, testCase := range testCases {
			t.Run(testCase.name, func(t *testing.T) {
				path := "/__aisys__/api/my-accounts/" + testCase.accountID +
					"/test-options/models/" + testCase.defaultModel + "?systemAccountId=sys_w2_test_options_grantee"
				rec := requestW2AccountTestOptions(t, router, path, w2AccountTestOptionsOwnerToken)
				result := decodeW2AccountTestModelCapabilities(t, rec, http.StatusOK)
				if result.ID != testCase.defaultModel || !reflect.DeepEqual(result.TestEndpointModes, testCase.wantModes) {
					t.Fatalf("model capabilities = %+v, want model %q modes %+v", result, testCase.defaultModel, testCase.wantModes)
				}
			})
		}
	})

	t.Run("hybrid model mappings produce per-model modes and filter empty models", func(t *testing.T) {
		rec := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/my-accounts/acct_w2_test_hybrid/test-options",
			w2AccountTestOptionsOwnerToken,
		)
		options := decodeW2AccountTestSelectionOptions(t, rec, http.StatusOK)
		for _, model := range []string{"w2-hybrid-owner-model", "w2-hybrid-chat-upstream", "w2-hybrid-reduced-model", "w2-hybrid-filtered-model"} {
			assertW2AccountTestSelectionContains(t, options, model)
		}
		for model, wantModes := range map[string][]string{
			"w2-hybrid-owner-model":   {"messages_sse"},
			"w2-hybrid-chat-upstream": {"chat_json"},
			"w2-hybrid-reduced-model": {"chat_json"},
		} {
			capabilities := decodeW2AccountTestModelCapabilities(t, requestW2AccountTestOptions(
				t, router, "/__aisys__/api/my-accounts/acct_w2_test_hybrid/test-options/models/"+model, w2AccountTestOptionsOwnerToken,
			), http.StatusOK)
			if !reflect.DeepEqual(capabilities.TestEndpointModes, wantModes) {
				t.Fatalf("model %q modes = %+v, want %+v", model, capabilities.TestEndpointModes, wantModes)
			}
		}
		assertW2AccountTestOptionsMessage(t, requestW2AccountTestOptions(
			t, router, "/__aisys__/api/my-accounts/acct_w2_test_hybrid/test-options/models/w2-hybrid-filtered-model", w2AccountTestOptionsOwnerToken,
		), http.StatusBadRequest, "账户上游接口能力中没有可用于连接测试的请求形态")
	})

	t.Run("authorized instance uses source semantics and source catalog owner", func(t *testing.T) {
		source, found, err := store.GetManagementAccountTestOptionsSource(ctx, port.ManagementAccountTestOptionsInput{
			AccountID:       "acct_w2_test_authorized",
			SystemAccountID: "sys_w2_test_options_grantee",
		})
		if err != nil || !found {
			t.Fatalf("read authorized account test options source: found=%t err=%v", found, err)
		}
		if source.ID != "acct_w2_test_authorized" ||
			source.OwnerSystemAccountID != "sys_w2_test_options_source_owner" ||
			source.ProviderCode != "deepseek" ||
			source.ProviderProtocolProfileID != "profile_deepseek_openai_v1" ||
			source.ProtocolCode != "openai" ||
			source.ProtocolVersion != "v1" ||
			source.Type != "api_key" ||
			source.ClientCompatibility != "openai_standard" ||
			source.HealthCheckModel != "w2-source-owner-model" ||
			source.HealthCheckEndpointMode != "chat_sse" ||
			source.CredentialsEncrypted == w2AccountTestOptionsCorruptText {
			t.Fatalf("authorized account test options source = %+v", source)
		}
		credentials, err := codec.DecryptJSON(source.CredentialsEncrypted)
		if err != nil || credentials["api_key"] != "sk-authorized-source" {
			t.Fatalf("authorized source credentials = %#v, err=%v", credentials, err)
		}
		wantSourceMappings := []port.ManagementAccountTestModelMapping{
			{
				SourceModel:            "w2-source-owner-model",
				SourceEndpointFamily:   "chat_completions",
				UpstreamModel:          "w2-source-chat-upstream",
				UpstreamEndpointFamily: "chat_completions",
				Enabled:                true,
			},
		}
		if !reflect.DeepEqual(source.ModelMappings, wantSourceMappings) {
			t.Fatalf("authorized mappings = %#v, want source account mappings %#v", source.ModelMappings, wantSourceMappings)
		}

		rec := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/my-accounts/acct_w2_test_authorized/test-options?systemAccountId=sys_w2_test_options_source_owner",
			w2AccountTestOptionsGranteeToken,
		)
		options := decodeW2AccountTestSelectionOptions(t, rec, http.StatusOK)
		assertW2AccountTestSelectionContains(t, options, "w2-source-owner-model")
		assertW2AccountTestSelectionContains(t, options, "w2-source-chat-upstream")
		if findW2AccountTestSelectionOption(options, "w2-grantee-only-model") != nil {
			t.Fatalf("authorized response leaked grantee catalog: %+v", options)
		}
		capabilities := decodeW2AccountTestModelCapabilities(t, requestW2AccountTestOptions(
			t, router, "/__aisys__/api/my-accounts/acct_w2_test_authorized/test-options/models/w2-source-owner-model", w2AccountTestOptionsGranteeToken,
		), http.StatusOK)
		if !reflect.DeepEqual(capabilities.TestEndpointModes, []string{"chat_sse", "chat_json"}) {
			t.Fatalf("authorized source modes = %+v", capabilities.TestEndpointModes)
		}
	})

	t.Run("stale health model is a precise validation error", func(t *testing.T) {
		rec := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/my-accounts/acct_w2_test_stale/test-options",
			w2AccountTestOptionsOwnerToken,
		)
		if rec.Code != http.StatusOK {
			t.Fatalf("stale list status = %d, body = %s", rec.Code, rec.Body.String())
		}
		assertW2AccountTestOptionsMessage(t, requestW2AccountTestOptions(
			t, router, "/__aisys__/api/my-accounts/acct_w2_test_stale/test-options/models/w2-retired-model", w2AccountTestOptionsOwnerToken,
		), http.StatusBadRequest, "模型不在当前账户供应商可用目录中：w2-retired-model")
	})

	t.Run("corrupt ciphertext is generic and does not leak", func(t *testing.T) {
		rec := requestW2AccountTestOptions(
			t,
			router,
			"/__aisys__/api/my-accounts/acct_w2_test_corrupt/test-options",
			w2AccountTestOptionsOwnerToken,
		)
		if rec.Code != http.StatusOK {
			t.Fatalf("corrupt list status = %d, body = %s", rec.Code, rec.Body.String())
		}
		capabilities := requestW2AccountTestOptions(
			t, router, "/__aisys__/api/my-accounts/acct_w2_test_corrupt/test-options/models/w2-hybrid-owner-model", w2AccountTestOptionsOwnerToken,
		)
		assertW2AccountTestOptionsMessage(t, capabilities, http.StatusInternalServerError, "服务器内部错误")
		if strings.Contains(capabilities.Body.String(), w2AccountTestOptionsCorruptText) {
			t.Fatalf("corrupt ciphertext leaked in response: %s", capabilities.Body.String())
		}
	})

	assertW2AccountTestOptionsSessionsUntouched(t, ctx, db, sessionLastSeenAt)
}

func insertW2AccountTestOptionsFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	codec interface {
		EncryptJSON(value map[string]any) (string, error)
	},
	now time.Time,
	sessionLastSeenAt time.Time,
) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			('sys_w2_test_options_admin', 'w2-test-options-admin', 'W2 Test Options Admin', NULL, 'admin', 'active', 'hash', false, false, $1, $1),
			('sys_w2_test_options_owner', 'w2-test-options-owner', 'W2 Test Options Owner', NULL, 'user', 'active', 'hash', false, false, $1, $1),
			('sys_w2_test_options_source_owner', 'w2-test-options-source-owner', 'W2 Test Options Source Owner', NULL, 'user', 'active', 'hash', false, false, $1, $1),
			('sys_w2_test_options_grantee', 'w2-test-options-grantee', 'W2 Test Options Grantee', NULL, 'user', 'active', 'hash', false, false, $1, $1)
	`, now); err != nil {
		t.Fatalf("insert account test options system accounts: %v", err)
	}

	sessions := []struct {
		id              string
		systemAccountID string
		token           string
	}{
		{id: "session_w2_test_options_admin", systemAccountID: "sys_w2_test_options_admin", token: w2AccountTestOptionsAdminToken},
		{id: "session_w2_test_options_owner", systemAccountID: "sys_w2_test_options_owner", token: w2AccountTestOptionsOwnerToken},
		{id: "session_w2_test_options_grantee", systemAccountID: "sys_w2_test_options_grantee", token: w2AccountTestOptionsGranteeToken},
	}
	for _, session := range sessions {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.system_sessions (
				id, system_account_id, token_hash, expires_at, created_at, last_seen_at
			) VALUES ($1, $2, $3, $4, $5, $6)
		`,
			session.id,
			session.systemAccountID,
			managementauth.HashSessionToken(session.token),
			now.Add(time.Hour),
			now.Add(-time.Hour),
			sessionLastSeenAt,
		); err != nil {
			t.Fatalf("insert account test options session %s: %v", session.id, err)
		}
	}

	insertW2AccountTestOptionsCustomModels(t, ctx, db, now)

	encrypt := func(label string, endpointModes any) string {
		t.Helper()
		credentials := map[string]any{
			"api_key":  "sk-" + label,
			"base_url": "https://" + label + ".example.test/v1",
		}
		if endpointModes != nil {
			credentials["supported_endpoint_modes"] = endpointModes
		}
		encrypted, err := codec.EncryptJSON(credentials)
		if err != nil {
			t.Fatalf("encrypt account test options credentials %s: %v", label, err)
		}
		return encrypted
	}

	accounts := []w2AccountTestOptionsAccountFixture{
		{
			id: "acct_w2_test_hybrid", systemAccountID: "sys_w2_test_options_owner",
			providerCode: "hybrid", profileID: "profile_hybrid_openai_chat_v1", protocolCode: "openai", protocolVersion: "v1",
			name: "W2 Hybrid Test Options", clientCompatibility: "openai_standard",
			healthModel: "w2-hybrid-owner-model", healthMode: "messages_sse",
			credentialsEncrypted: encrypt("hybrid", []any{"messages_sse", "chat_json", "count_tokens", "generate_content_sse"}),
		},
		{
			id: "acct_w2_test_anthropic", systemAccountID: "sys_w2_test_options_owner",
			providerCode: "anthropic", profileID: "profile_anthropic_anthropic_v1", protocolCode: "anthropic", protocolVersion: "v1",
			name: "W2 Anthropic Test Options", clientCompatibility: "openai_standard",
			healthModel: "w2-anthropic-owner-model", healthMode: "messages_sse",
			credentialsEncrypted: encrypt("anthropic", nil),
		},
		{
			id: "acct_w2_test_gemini", systemAccountID: "sys_w2_test_options_owner",
			providerCode: "gemini", profileID: "profile_gemini_native_v1beta", protocolCode: "gemini", protocolVersion: "v1beta",
			name: "W2 Gemini Test Options", clientCompatibility: "openai_standard",
			healthModel: "w2-gemini-owner-model", healthMode: "generate_content_sse",
			credentialsEncrypted: encrypt("gemini", []any{"count_tokens", "generate_content_json"}),
		},
		{
			id: "acct_w2_test_gpt_oauth", systemAccountID: "sys_w2_test_options_owner",
			providerCode: "gpt", profileID: "profile_gpt_openai_v1", protocolCode: "openai", protocolVersion: "v1",
			name: "W2 GPT OAuth Test Options", accountType: "oauth", clientCompatibility: "codex_responses",
			healthModel: "w2-gpt-oauth-model", healthMode: "responses_sse",
			credentialsEncrypted: encrypt("gpt-oauth", nil),
		},
		{
			id: "acct_w2_test_stale", systemAccountID: "sys_w2_test_options_owner",
			providerCode: "anthropic", profileID: "profile_anthropic_anthropic_v1", protocolCode: "anthropic", protocolVersion: "v1",
			name: "W2 Stale Test Options", clientCompatibility: "openai_standard",
			healthModel: "w2-retired-model", healthMode: "messages_json",
			credentialsEncrypted: encrypt("stale", []any{"messages_json"}),
		},
		{
			id: "acct_w2_test_corrupt", systemAccountID: "sys_w2_test_options_owner",
			providerCode: "hybrid", profileID: "profile_hybrid_openai_chat_v1", protocolCode: "openai", protocolVersion: "v1",
			name: "W2 Corrupt Test Options", clientCompatibility: "openai_standard",
			healthModel: "w2-hybrid-owner-model", healthMode: "chat_json",
			credentialsEncrypted: w2AccountTestOptionsCorruptText,
		},
		{
			id: "acct_w2_test_source", systemAccountID: "sys_w2_test_options_source_owner",
			providerCode: "deepseek", profileID: "profile_deepseek_openai_v1", protocolCode: "openai", protocolVersion: "v1",
			name: "W2 Authorized Source", clientCompatibility: "openai_standard",
			healthModel: "w2-source-owner-model", healthMode: "chat_json",
			credentialsEncrypted: encrypt("authorized-source", nil),
		},
	}
	for index, account := range accounts {
		insertW2AccountTestOptionsAccount(t, ctx, db, account, now.Add(time.Duration(index)*time.Second))
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id,
			grantee_system_account_id, scope, status, effective_source_type,
			activated_at, created_by, created_at, updated_at
		) VALUES (
			'auth_w2_test_options_account', 'account', 'acct_w2_test_source',
			'sys_w2_test_options_source_owner', 'sys_w2_test_options_grantee',
			'use', 'active', 'manual', $1, 'sys_w2_test_options_source_owner', $1, $1
		)
	`, now); err != nil {
		t.Fatalf("insert account test options authorization: %v", err)
	}

	insertW2AccountTestOptionsAccount(t, ctx, db, w2AccountTestOptionsAccountFixture{
		id: "acct_w2_test_authorized", systemAccountID: "sys_w2_test_options_grantee",
		providerCode: "gemini", profileID: "profile_gemini_native_v1beta", protocolCode: "gemini", protocolVersion: "v1beta",
		name: "W2 Authorized Account Instance", clientCompatibility: "codex_responses",
		healthModel: "w2-source-owner-model", healthMode: "chat_sse",
		credentialsEncrypted: w2AccountTestOptionsCorruptText,
		sourceAccountID:      "acct_w2_test_source", authorizationID: "auth_w2_test_options_account",
		sourceOwnerSystemAccountID: "sys_w2_test_options_source_owner",
	}, now.Add(20*time.Second))

	insertW2AccountTestOptionsModelMappings(t, ctx, db, now.Add(30*time.Second))
}

type w2AccountTestOptionsAccountFixture struct {
	id                         string
	systemAccountID            string
	providerCode               string
	profileID                  string
	protocolCode               string
	protocolVersion            string
	name                       string
	accountType                string
	clientCompatibility        string
	healthModel                string
	healthMode                 string
	credentialsEncrypted       string
	sourceAccountID            string
	authorizationID            string
	sourceOwnerSystemAccountID string
}

func insertW2AccountTestOptionsAccount(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	account w2AccountTestOptionsAccountFixture,
	now time.Time,
) {
	t.Helper()
	accountType := strings.TrimSpace(account.accountType)
	if accountType == "" {
		accountType = "api_key"
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status, credentials_encrypted,
			credential_fingerprint, credential_mask, concurrency_limit, priority,
			super_priority_enabled, fallback_enabled, client_compatibility, schedulable,
			health_check_model, health_check_endpoint_mode,
			authorization_instance_source_account_id, authorization_instance_authorization_id,
			authorization_instance_owner_system_account_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, 'active', $9,
			NULL, '', 20, 0,
			false, false, $10, true,
			$11, $12,
			NULLIF($13, ''), NULLIF($14, ''), NULLIF($15, ''), $16, $16
		)
	`,
		account.id,
		account.systemAccountID,
		account.providerCode,
		account.profileID,
		account.protocolCode,
		account.protocolVersion,
		account.name,
		accountType,
		account.credentialsEncrypted,
		account.clientCompatibility,
		account.healthModel,
		account.healthMode,
		account.sourceAccountID,
		account.authorizationID,
		account.sourceOwnerSystemAccountID,
		now,
	); err != nil {
		t.Fatalf("insert account test options account %s: %v", account.id, err)
	}
}

func insertW2AccountTestOptionsModelMappings(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_model_mappings (
			account_id, provider_code, source_model, source_endpoint_family,
			upstream_model, upstream_endpoint_family, enabled, created_at, updated_at
		) VALUES
			(
				'acct_w2_test_hybrid', 'hybrid', 'w2-hybrid-owner-model', 'messages',
				'w2-hybrid-chat-upstream', 'chat_completions', true, $1, $1
			),
			(
				'acct_w2_test_hybrid', 'hybrid', 'w2-hybrid-reduced-model', 'messages',
				'w2-hybrid-responses-target', 'chat_completions', true, $1, $1
			),
			(
				'acct_w2_test_hybrid', 'hybrid', 'w2-hybrid-filtered-model', 'messages',
				'w2-hybrid-responses-target', 'chat_completions', true, $1, $1
			),
			(
				'acct_w2_test_source', 'deepseek', 'w2-source-owner-model', 'chat_completions',
				'w2-source-chat-upstream', 'chat_completions', true, $1, $1
			),
			(
				'acct_w2_test_authorized', 'gemini', 'w2-source-owner-model', 'chat_completions',
				'w2-source-responses-target', 'chat_completions', true, $1, $1
			)
	`, now); err != nil {
		t.Fatalf("insert account test options model mappings: %v", err)
	}
}

func insertW2AccountTestOptionsCustomModels(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	fixtures := []struct {
		id              string
		providerCode    string
		model           string
		systemAccountID string
		protocolsJSON   string
	}{
		{id: "custom_w2_test_hybrid", providerCode: "gpt", model: "w2-hybrid-owner-model", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["messages"]`},
		{id: "custom_w2_test_hybrid_chat_upstream", providerCode: "deepseek", model: "w2-hybrid-chat-upstream", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["chat_completions"]`},
		{id: "custom_w2_test_hybrid_reduced", providerCode: "gpt", model: "w2-hybrid-reduced-model", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["messages","chat_completions"]`},
		{id: "custom_w2_test_hybrid_filtered", providerCode: "gpt", model: "w2-hybrid-filtered-model", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["messages"]`},
		{id: "custom_w2_test_hybrid_responses_target", providerCode: "gpt", model: "w2-hybrid-responses-target", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["responses"]`},
		{id: "custom_w2_test_anthropic", providerCode: "anthropic", model: "w2-anthropic-owner-model", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["messages"]`},
		{id: "custom_w2_test_gemini", providerCode: "gemini", model: "w2-gemini-owner-model", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["generate_content"]`},
		{id: "custom_w2_test_gpt_oauth", providerCode: "gpt", model: "w2-gpt-oauth-model", systemAccountID: "sys_w2_test_options_owner", protocolsJSON: `["responses"]`},
		{id: "custom_w2_test_source", providerCode: "deepseek", model: "w2-source-owner-model", systemAccountID: "sys_w2_test_options_source_owner", protocolsJSON: `["chat_completions"]`},
		{id: "custom_w2_test_source_chat_upstream", providerCode: "deepseek", model: "w2-source-chat-upstream", systemAccountID: "sys_w2_test_options_source_owner", protocolsJSON: `["chat_completions"]`},
		{id: "custom_w2_test_source_responses_target", providerCode: "deepseek", model: "w2-source-responses-target", systemAccountID: "sys_w2_test_options_source_owner", protocolsJSON: `["responses"]`},
		{id: "custom_w2_test_grantee", providerCode: "deepseek", model: "w2-grantee-only-model", systemAccountID: "sys_w2_test_options_grantee", protocolsJSON: `["chat_completions"]`},
	}
	for index, fixture := range fixtures {
		createdAt := now.Add(time.Duration(index) * time.Second)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.custom_provider_models (
				id, provider_code, model, scope, system_account_id, status, mode,
				supported_api_protocols_json, created_by, created_at, updated_at
			) VALUES (
				$1, $2, $3, 'personal', $4, 'active', 'chat',
				$5, $4, $6, $6
			)
		`,
			fixture.id,
			fixture.providerCode,
			fixture.model,
			fixture.systemAccountID,
			fixture.protocolsJSON,
			createdAt,
		); err != nil {
			t.Fatalf("insert account test options custom model %s: %v", fixture.id, err)
		}
	}
}

func requestW2AccountTestOptions(
	t *testing.T,
	router http.Handler,
	target string,
	sessionToken string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if sessionToken != "" {
		req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", target, got)
	}
	return rec
}

func decodeW2AccountTestOptionsResult(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
) managementaccounttestoptions.Result {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("account test options status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body struct {
		Data managementaccounttestoptions.Result `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode account test options response: %v", err)
	}
	return body.Data
}

func decodeW2AccountTestSelectionOptions(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
) []managementaccounttestoptions.SelectionOption {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("account test options status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body struct {
		Data []managementaccounttestoptions.SelectionOption `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode account test selection options response: %v", err)
	}
	return body.Data
}

func decodeW2AccountTestModelCapabilities(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
) managementaccounttestoptions.ModelCapabilities {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("account test model capabilities status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body struct {
		Data managementaccounttestoptions.ModelCapabilities `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode account test model capabilities response: %v", err)
	}
	return body.Data
}

func assertW2AccountTestSelectionContains(
	t *testing.T,
	options []managementaccounttestoptions.SelectionOption,
	model string,
) {
	t.Helper()
	if findW2AccountTestSelectionOption(options, model) == nil {
		t.Fatalf("account test selection options missing %q: %+v", model, options)
	}
}

func findW2AccountTestSelectionOption(
	options []managementaccounttestoptions.SelectionOption,
	model string,
) *managementaccounttestoptions.SelectionOption {
	for index := range options {
		if options[index].ID == model {
			return &options[index]
		}
	}
	return nil
}

func assertW2AccountTestOptionsResult(
	t *testing.T,
	result managementaccounttestoptions.Result,
	wantAccountID string,
	wantDefaultModel string,
	wantModes []string,
) {
	t.Helper()
	if result.AccountID != wantAccountID || result.DefaultModel != wantDefaultModel {
		t.Fatalf("account test options identity = %+v, want account %q model %q", result, wantAccountID, wantDefaultModel)
	}
	if !reflect.DeepEqual(result.TestEndpointModes, wantModes) {
		t.Fatalf("account test options modes = %+v, want %+v", result.TestEndpointModes, wantModes)
	}
	if len(wantModes) == 0 || result.DefaultTestEndpointMode != wantModes[0] {
		t.Fatalf("account test options default endpoint mode = %q, want %q", result.DefaultTestEndpointMode, wantModes[0])
	}
	if findW2AccountTestOptionsModel(result.Models, wantDefaultModel) == nil {
		t.Fatalf("account test options models missing default %q: %+v", wantDefaultModel, result.Models)
	}
	defaultModel := findW2AccountTestOptionsModel(result.Models, wantDefaultModel)
	if !reflect.DeepEqual(result.TestEndpointModes, defaultModel.TestEndpointModes) {
		t.Fatalf(
			"account test options top-level modes = %+v, default model modes = %+v",
			result.TestEndpointModes,
			defaultModel.TestEndpointModes,
		)
	}
	for _, model := range result.Models {
		if len(model.TestEndpointModes) == 0 {
			t.Fatalf("account test options model has no test endpoint modes: %+v", model)
		}
	}
}

func assertW2AccountTestOptionsModelModes(
	t *testing.T,
	result managementaccounttestoptions.Result,
	model string,
	want []string,
) {
	t.Helper()
	option := findW2AccountTestOptionsModel(result.Models, model)
	if option == nil {
		t.Fatalf("account test options models missing %q: %+v", model, result.Models)
	}
	if !reflect.DeepEqual(option.TestEndpointModes, want) {
		t.Fatalf("account test options model %q modes = %+v, want %+v", model, option.TestEndpointModes, want)
	}
}

func findW2AccountTestOptionsModel(
	models []managementaccounttestoptions.ModelOption,
	model string,
) *managementaccounttestoptions.ModelOption {
	for index := range models {
		if models[index].Model == model {
			return &models[index]
		}
	}
	return nil
}

func assertW2AccountTestOptionsMessage(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("account test options status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode account test options error: %v", err)
	}
	if body.Message != wantMessage {
		t.Fatalf("account test options message = %q, want %q", body.Message, wantMessage)
	}
}

func assertW2AccountTestOptionsSessionsUntouched(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	want time.Time,
) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT id, last_seen_at
		FROM juhe_business.system_sessions
		WHERE id LIKE 'session_w2_test_options_%'
		ORDER BY id ASC
	`)
	if err != nil {
		t.Fatalf("read account test options sessions: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id string
		var lastSeenAt time.Time
		if err := rows.Scan(&id, &lastSeenAt); err != nil {
			t.Fatalf("scan account test options session: %v", err)
		}
		if !lastSeenAt.Equal(want) {
			t.Fatalf("read-only account test options request touched session %s: last_seen_at=%s want=%s", id, lastSeenAt.UTC().Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate account test options sessions: %v", err)
	}
	if count != 3 {
		t.Fatalf("account test options session count = %d, want 3", count)
	}
}
