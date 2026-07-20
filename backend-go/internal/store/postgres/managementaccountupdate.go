package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) LoadManagementAccountUpdateTarget(ctx context.Context, input port.ManagementAccountUpdateTargetInput) (port.ManagementAccountUpdateTarget, bool, error) {
	var target port.ManagementAccountUpdateTarget
	err := s.pool.QueryRow(ctx, managementAccountUpdateTargetSQL,
		strings.TrimSpace(input.AccountID), input.CanAccessAll, strings.TrimSpace(input.EffectiveSystemAccountID),
	).Scan(
		&target.ID, &target.SystemAccountID, &target.OwnerSystemAccountID, &target.AccessType,
		&target.ProviderCode, &target.ProviderProfileID, &target.Type, &target.ConfigRevision,
		&target.CredentialsEncrypted, &target.Status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountUpdateTarget{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountUpdateTarget{}, false, fmt.Errorf("load management account update target: %w", err)
	}
	return target, true, nil
}

func (s *Store) UpdateManagementAccount(ctx context.Context, input port.ManagementAccountUpdateInput) (port.ManagementAccountUpdateResult, bool, error) {
	patchJSON, err := json.Marshal(input.Updates)
	if err != nil {
		return port.ManagementAccountUpdateResult{}, false, fmt.Errorf("encode management account update patch: %w", err)
	}
	var beforeJSON, afterJSON, accountID, systemAccountID string
	err = s.pool.QueryRow(ctx, managementAccountUpdateSQL,
		strings.TrimSpace(input.AccountID), input.CanAccessAll, strings.TrimSpace(input.EffectiveSystemAccountID),
		input.ExpectedConfigRevision, string(patchJSON), input.HasCredentials, input.CredentialsEncrypted, input.UpdatedAt,
	).Scan(&beforeJSON, &afterJSON, &accountID, &systemAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountUpdateResult{}, false, nil
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "accounts_owner_name") {
			return port.ManagementAccountUpdateResult{}, false, port.ErrManagementAccountUpdateNameExists
		}
		return port.ManagementAccountUpdateResult{}, false, fmt.Errorf("update management account: %w", err)
	}
	before := map[string]any{}
	after := map[string]any{}
	if err := json.Unmarshal([]byte(beforeJSON), &before); err != nil {
		return port.ManagementAccountUpdateResult{}, false, fmt.Errorf("decode management account before update: %w", err)
	}
	if err := json.Unmarshal([]byte(afterJSON), &after); err != nil {
		return port.ManagementAccountUpdateResult{}, false, fmt.Errorf("decode management account after update: %w", err)
	}
	changedFields := make([]string, 0, len(input.Updates)+1)
	for field := range input.Updates {
		changedFields = append(changedFields, field)
	}
	if input.HasCredentials {
		changedFields = append(changedFields, "credentials")
	}
	sort.Strings(changedFields)
	return port.ManagementAccountUpdateResult{
		AccountID: accountID, SystemAccountID: systemAccountID, OwnerSystemAccountID: systemAccountID,
		Before: before, After: after, ChangedFields: changedFields,
	}, true, nil
}

const managementAccountUpdateTargetSQL = `
SELECT
  accounts.id,
  accounts.system_account_id,
  COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id),
  CASE WHEN accounts.authorization_instance_authorization_id IS NULL THEN 'owner' ELSE 'authorized' END,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.type,
  accounts.config_revision,
  accounts.credentials_encrypted,
  accounts.status
FROM juhe_business.accounts AS accounts
WHERE accounts.id = $1
  AND ($2::boolean OR accounts.system_account_id = $3)
  AND accounts.deleted_at IS NULL
LIMIT 1`

const managementAccountUpdateSQL = `
WITH current_target AS MATERIALIZED (
  SELECT accounts.*
  FROM juhe_business.accounts AS accounts
  INNER JOIN juhe_business.providers AS providers
    ON providers.code = accounts.provider_code AND providers.enabled = true
  INNER JOIN juhe_business.provider_protocol_profiles AS profiles
    ON profiles.id = accounts.provider_protocol_profile_id
    AND profiles.provider_code = accounts.provider_code
    AND profiles.enabled = true
    AND profiles.account_types_json::jsonb ? accounts.type
  WHERE accounts.id = $1
    AND ($2::boolean OR accounts.system_account_id = $3)
    AND accounts.config_revision = $4
    AND accounts.deleted_at IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
  FOR UPDATE OF accounts
), group_guard AS MATERIALIZED (
  SELECT current_target.id,
    CASE WHEN NOT ($5::jsonb ? 'groupId') THEN true ELSE EXISTS (
      SELECT 1 FROM juhe_business.groups AS groups
      WHERE groups.id = $5::jsonb ->> 'groupId'
        AND groups.system_account_id = current_target.system_account_id
        AND groups.provider_code = current_target.provider_code
        AND groups.enabled = true
    ) END AS valid
  FROM current_target
), updated_account AS (
  UPDATE juhe_business.accounts AS accounts
  SET
    name = CASE WHEN $5::jsonb ? 'name' THEN btrim($5::jsonb ->> 'name') ELSE accounts.name END,
    notes = CASE WHEN $5::jsonb ? 'notes' THEN $5::jsonb ->> 'notes' ELSE accounts.notes END,
    status = CASE
      WHEN COALESCE(($5::jsonb ->> 'clearFailureState')::boolean, false) THEN CASE WHEN accounts.status = 'pending_test' THEN 'pending_test' ELSE 'active' END
      WHEN $5::jsonb ? 'status' THEN $5::jsonb ->> 'status'
      ELSE accounts.status
    END,
    credentials_encrypted = CASE WHEN $6::boolean THEN $7 ELSE accounts.credentials_encrypted END,
    concurrency_limit = CASE WHEN $5::jsonb ? 'concurrencyLimit' THEN ($5::jsonb ->> 'concurrencyLimit')::integer ELSE accounts.concurrency_limit END,
    priority = CASE WHEN $5::jsonb ? 'priority' THEN ($5::jsonb ->> 'priority')::integer ELSE accounts.priority END,
    super_priority_enabled = CASE WHEN $5::jsonb ? 'superPriorityEnabled' THEN ($5::jsonb ->> 'superPriorityEnabled')::boolean ELSE accounts.super_priority_enabled END,
    fallback_enabled = CASE WHEN $5::jsonb ? 'fallbackEnabled' THEN ($5::jsonb ->> 'fallbackEnabled')::boolean ELSE accounts.fallback_enabled END,
    proxy_profile_id = CASE WHEN $5::jsonb ? 'proxyProfileId' THEN NULLIF($5::jsonb ->> 'proxyProfileId', '') ELSE accounts.proxy_profile_id END,
    schedulable = CASE WHEN $5::jsonb ? 'schedulable' THEN ($5::jsonb ->> 'schedulable')::boolean ELSE accounts.schedulable END,
    health_check_model = CASE WHEN $5::jsonb ? 'healthCheckModel' THEN $5::jsonb ->> 'healthCheckModel' ELSE accounts.health_check_model END,
    health_check_endpoint_mode = CASE WHEN $5::jsonb ? 'healthCheckEndpointMode' THEN $5::jsonb ->> 'healthCheckEndpointMode' ELSE accounts.health_check_endpoint_mode END,
    temporary_unavailable_continuous_probe_enabled = CASE WHEN $5::jsonb ? 'temporaryUnavailableContinuousProbeEnabled' THEN ($5::jsonb ->> 'temporaryUnavailableContinuousProbeEnabled')::boolean ELSE accounts.temporary_unavailable_continuous_probe_enabled END,
    cooldown_until = CASE WHEN COALESCE(($5::jsonb ->> 'clearFailureState')::boolean, false) THEN NULL ELSE accounts.cooldown_until END,
    last_error_code = CASE WHEN COALESCE(($5::jsonb ->> 'clearFailureState')::boolean, false) THEN NULL ELSE accounts.last_error_code END,
    last_error_message = CASE WHEN COALESCE(($5::jsonb ->> 'clearFailureState')::boolean, false) THEN NULL ELSE accounts.last_error_message END,
    config_revision = config_revision + 1,
    updated_at = $8
  FROM current_target, group_guard
  WHERE accounts.id = current_target.id AND group_guard.valid
  RETURNING
    jsonb_strip_nulls(jsonb_build_object(
      'id', current_target.id, 'systemAccountId', current_target.system_account_id,
      'ownerSystemAccountId', current_target.system_account_id, 'name', current_target.name,
      'providerCode', current_target.provider_code, 'type', current_target.type,
      'status', current_target.status, 'configRevision', current_target.config_revision
    ))::text AS before_json,
    jsonb_strip_nulls(jsonb_build_object(
      'id', accounts.id, 'systemAccountId', accounts.system_account_id,
      'ownerSystemAccountId', accounts.system_account_id, 'name', accounts.name,
      'providerCode', accounts.provider_code, 'type', accounts.type,
      'status', accounts.status, 'configRevision', accounts.config_revision
    ))::text AS after_json,
    accounts.id,
    accounts.system_account_id
)
SELECT before_json, after_json, id, system_account_id
FROM updated_account`

var _ port.ManagementAccountUpdater = (*Store)(nil)
