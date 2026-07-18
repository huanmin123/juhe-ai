//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW4ManagementAuthorizationAccountHealthCheckModelPostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	insertW4AuthorizationAccountHealthModelFixtures(t, ctx, db, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	createInput := port.ManagementResourceAuthorizationCreateInput{
		ResourceType:                    "account",
		ResourceID:                      "acc_w4_auth_health_source",
		ResourceOwnerSystemAccountID:    "sys_w4_auth_health_owner",
		GranteeType:                     "system_account",
		GranteeID:                       "sys_w4_auth_health_grantee",
		TargetGroupID:                   "grp_w4_auth_health_grantee_default",
		AuthorizationInstanceSecretJSON: "w4-authorization-instance-secret",
		ActorSystemAccountID:            "sys_w4_auth_health_owner",
		CreatedAt:                       now.Add(time.Minute),
	}
	created, err := store.CreateManagementResourceAuthorization(ctx, createInput)
	if err != nil {
		t.Fatalf("create W4 account authorization: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created W4 account authorization grant id is empty")
	}

	initial := readW4AuthorizationAccountHealthModelInstance(t, ctx, db)
	assertW4AuthorizationAccountHealthModelInstance(
		t,
		initial,
		"",
		"w4-source-health-model-v1",
		"chat_json",
		false,
	)

	if _, found, err := store.ReturnManagementResourceAuthorizationForGrantee(ctx, port.ManagementResourceAuthorizationReturnInput{
		AuthorizationID:        created.ID,
		GranteeSystemAccountID: "sys_w4_auth_health_grantee",
		ActorSystemAccountID:   "sys_w4_auth_health_grantee",
		ReturnedAt:             now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("return W4 account authorization: %v", err)
	} else if !found {
		t.Fatal("return W4 account authorization = not found")
	}

	deleted, err := store.DeletePublicAccount(
		ctx,
		initial.ID,
		"sys_w4_auth_health_grantee",
		"sys_w4_auth_health_grantee",
		now.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatalf("soft delete W4 authorization account instance: %v", err)
	}
	if !deleted {
		t.Fatal("soft delete W4 authorization account instance = false")
	}
	deletedInstance := readW4AuthorizationAccountHealthModelInstance(t, ctx, db)
	assertW4AuthorizationAccountHealthModelInstance(
		t,
		deletedInstance,
		initial.ID,
		"w4-source-health-model-v1",
		"chat_json",
		true,
	)

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET health_check_model = $1,
		    temporary_unavailable_continuous_probe_enabled = false,
		    updated_at = $2
		WHERE id = 'acc_w4_auth_health_source'
	`, "w4-source-health-model-v2", now.Add(4*time.Minute)); err != nil {
		t.Fatalf("update W4 source account health_check_model: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.accounts
		SET temporary_unavailable_continuous_probe_enabled = true,
		    updated_at = $1
		WHERE id = $2
	`, now.Add(4*time.Minute), initial.ID); err != nil {
		t.Fatalf("set deleted W4 authorization instance probe flag: %v", err)
	}

	createInput.CreatedAt = now.Add(5 * time.Minute)
	restored, err := store.CreateManagementResourceAuthorization(ctx, createInput)
	if err != nil {
		t.Fatalf("recreate W4 account authorization: %v", err)
	}
	if restored.ID != created.ID {
		t.Fatalf("restored W4 account authorization grant id = %q, want %q", restored.ID, created.ID)
	}

	restoredInstance := readW4AuthorizationAccountHealthModelInstance(t, ctx, db)
	assertW4AuthorizationAccountHealthModelInstance(
		t,
		restoredInstance,
		initial.ID,
		"w4-source-health-model-v2",
		"chat_json",
		false,
	)
	if restoredInstance.AuthorizationID != initial.AuthorizationID {
		t.Fatalf(
			"restored W4 authorization runtime id = %q, want %q",
			restoredInstance.AuthorizationID,
			initial.AuthorizationID,
		)
	}
}

func insertW4AuthorizationAccountHealthModelFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			(
				'sys_w4_auth_health_owner', 'w4-auth-health-owner', 'W4 Authorization Owner',
				NULL, 'user', 'active', 'hash', false, false, $1, $1
			),
			(
				'sys_w4_auth_health_grantee', 'w4-auth-health-grantee', 'W4 Authorization Grantee',
				NULL, 'user', 'active', 'hash', false, false, $1, $1
			)
	`, now); err != nil {
		t.Fatalf("insert W4 authorization system account fixtures: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES (
			'grp_w4_auth_health_grantee_default',
			'sys_w4_auth_health_grantee',
			'W4 Authorization Default Group',
			'openai',
			NULL,
			true,
			true,
			'personal',
			NULL,
			$1,
			$1
		)
	`, now); err != nil {
		t.Fatalf("insert W4 authorization target group fixture: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status,
			credentials_encrypted, credential_fingerprint, credential_mask,
			concurrency_limit, priority, super_priority_enabled, fallback_enabled,
			schedulable, health_check_model, health_check_endpoint_mode,
			temporary_unavailable_continuous_probe_enabled, created_at, updated_at
		) VALUES (
			'acc_w4_auth_health_source',
			'sys_w4_auth_health_owner',
			'openai',
			'profile_openai_openai_v1',
			'openai',
			'v1',
			'W4 Authorization Source Account',
			'api_key',
			'active',
			'w4-source-secret',
			NULL,
			'',
			37,
			0,
			false,
			false,
			true,
			'w4-source-health-model-v1',
			'chat_json',
			false,
			$1,
			$1
		)
	`, now); err != nil {
		t.Fatalf("insert W4 authorization source account fixture: %v", err)
	}
}

type w4AuthorizationAccountHealthModelInstance struct {
	ID                                         string
	SystemAccountID                            string
	HealthCheckModel                           string
	HealthCheckEndpointMode                    string
	SourceAccountID                            string
	AuthorizationID                            string
	OwnerSystemAccountID                       string
	Status                                     string
	Schedulable                                bool
	TemporaryUnavailableContinuousProbeEnabled bool
	DeletedAt                                  sql.NullTime
}

func readW4AuthorizationAccountHealthModelInstance(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) w4AuthorizationAccountHealthModelInstance {
	t.Helper()

	var row w4AuthorizationAccountHealthModelInstance
	if err := db.QueryRowContext(ctx, `
		SELECT
			id,
			system_account_id,
			health_check_model,
			health_check_endpoint_mode,
			authorization_instance_source_account_id,
			authorization_instance_authorization_id,
			authorization_instance_owner_system_account_id,
			status,
			schedulable,
			temporary_unavailable_continuous_probe_enabled,
			deleted_at
		FROM juhe_business.accounts
		WHERE authorization_instance_source_account_id = 'acc_w4_auth_health_source'
		  AND system_account_id = 'sys_w4_auth_health_grantee'
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`).Scan(
		&row.ID,
		&row.SystemAccountID,
		&row.HealthCheckModel,
		&row.HealthCheckEndpointMode,
		&row.SourceAccountID,
		&row.AuthorizationID,
		&row.OwnerSystemAccountID,
		&row.Status,
		&row.Schedulable,
		&row.TemporaryUnavailableContinuousProbeEnabled,
		&row.DeletedAt,
	); err != nil {
		t.Fatalf("read W4 authorization account instance: %v", err)
	}
	return row
}

func assertW4AuthorizationAccountHealthModelInstance(
	t *testing.T,
	row w4AuthorizationAccountHealthModelInstance,
	wantID string,
	wantHealthCheckModel string,
	wantHealthCheckEndpointMode string,
	wantDeleted bool,
) {
	t.Helper()

	if wantID != "" && row.ID != wantID {
		t.Fatalf("W4 authorization account instance id = %q, want %q", row.ID, wantID)
	}
	if row.ID == "" ||
		row.SystemAccountID != "sys_w4_auth_health_grantee" ||
		row.HealthCheckModel != wantHealthCheckModel ||
		row.HealthCheckEndpointMode != wantHealthCheckEndpointMode ||
		row.SourceAccountID != "acc_w4_auth_health_source" ||
		row.AuthorizationID == "" ||
		row.OwnerSystemAccountID != "sys_w4_auth_health_owner" ||
		row.TemporaryUnavailableContinuousProbeEnabled ||
		row.DeletedAt.Valid != wantDeleted {
		t.Fatalf(
			"W4 authorization account instance = %+v, want health_check_model %q, endpoint mode %q and deleted %t",
			row,
			wantHealthCheckModel,
			wantHealthCheckEndpointMode,
			wantDeleted,
		)
	}
	if wantDeleted {
		if row.Status != "disabled" || row.Schedulable {
			t.Fatalf("deleted W4 authorization account instance status = %q schedulable = %t", row.Status, row.Schedulable)
		}
		return
	}
	if row.Status != "active" || !row.Schedulable {
		t.Fatalf("active W4 authorization account instance status = %q schedulable = %t", row.Status, row.Schedulable)
	}
}
