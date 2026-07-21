package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"juhe-ai/backend-go/internal/store/port"
)

const managementAccountTestDispatchResolveSQL = `
WITH dispatch_accounts AS (
  SELECT accounts.id, accounts.name,
         accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code, accounts.protocol_version,
         accounts.type, 'owner'::text AS access_type,
         accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.system_account_id AS access_system_account_id
  FROM juhe_business.accounts AS accounts
  WHERE accounts.deleted_at IS NULL
    AND accounts.authorization_instance_source_account_id IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
    AND accounts.authorization_instance_owner_system_account_id IS NULL

  UNION ALL

  SELECT accounts.id, accounts.name,
         source_accounts.provider_code, source_accounts.provider_protocol_profile_id, source_accounts.protocol_code, source_accounts.protocol_version,
         source_accounts.type, 'authorized'::text AS access_type,
         accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.system_account_id AS access_system_account_id
  FROM juhe_business.accounts AS accounts
  INNER JOIN juhe_business.accounts AS source_accounts
    ON source_accounts.id = accounts.authorization_instance_source_account_id
    AND source_accounts.deleted_at IS NULL
    AND source_accounts.authorization_instance_source_account_id IS NULL
    AND source_accounts.authorization_instance_authorization_id IS NULL
    AND source_accounts.authorization_instance_owner_system_account_id IS NULL
  INNER JOIN juhe_business.resource_authorizations AS resource_authorizations
    ON resource_authorizations.id = accounts.authorization_instance_authorization_id
    AND resource_authorizations.resource_type = 'account'
    AND resource_authorizations.resource_id = accounts.authorization_instance_source_account_id
    AND resource_authorizations.resource_owner_system_account_id = source_accounts.system_account_id
    AND (
      accounts.authorization_instance_owner_system_account_id IS NULL
      OR accounts.authorization_instance_owner_system_account_id = resource_authorizations.resource_owner_system_account_id
    )
    AND resource_authorizations.grantee_system_account_id = accounts.system_account_id
    AND resource_authorizations.status = 'active'
    AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > now())
  WHERE accounts.deleted_at IS NULL
)
SELECT id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
       type, access_type, health_check_model, health_check_endpoint_mode
FROM dispatch_accounts
WHERE id = $1
  AND ($2 = '' OR access_system_account_id = $2)`

const managementAccountTestDispatchCreateSQL = `
INSERT INTO juhe_business.account_test_tasks
 (id, request_system_account_id, request_role, request_system_account_filter_id, account_id, account_name, provider_code,
  provider_protocol_profile_id, protocol_code, protocol_version, account_type, status, status_message,
  diagnostics, model, test_endpoint_mode, draft_account_encrypted, cancel_requested, created_at, queued_at, updated_at)
VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, $10, $11, 'queued', '等待后台测试', $12, $13, $14, NULLIF($15, ''), false, now(), now(), now())
RETURNING id, '', account_id, account_name, provider_code, COALESCE(provider_protocol_profile_id,''), COALESCE(protocol_code,''),
COALESCE(protocol_version,''), account_type, status, COALESCE(status_message,''), COALESCE(model,''), COALESCE(test_endpoint_mode,''),
COALESCE(result_json,''), cancel_requested, created_at, queued_at, started_at, finished_at, updated_at`

const managementAccountTestDispatchSessionSQL = `
INSERT INTO juhe_business.account_test_session_tasks (session_id, task_id, created_at)
SELECT s.id, $2, now() FROM juhe_business.account_test_sessions s
WHERE s.id = $1 AND s.status = 'running'`

const managementAccountTestDispatchFailSQL = `
UPDATE juhe_business.account_test_tasks SET status = 'failed', status_message = $3,
 finished_at = COALESCE(finished_at, now()), updated_at = now()
WHERE id = $1 AND request_system_account_id = $2 AND status = 'queued'`

func (s *Store) ResolveManagementAccountTestAccount(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestDispatchAccount, bool, error) {
	var result port.ManagementAccountTestDispatchAccount
	err := s.pool.QueryRow(ctx, managementAccountTestDispatchResolveSQL, strings.TrimSpace(id), strings.TrimSpace(access.FilterSystemAccountID)).Scan(
		&result.ID, &result.Name, &result.ProviderCode, &result.ProviderProtocolProfileID, &result.ProtocolCode, &result.ProtocolVersion,
		&result.Type, &result.AccessType, &result.HealthCheckModel, &result.HealthCheckEndpointMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, fmt.Errorf("resolve account test target: %w", err)
	}
	return result, true, nil
}

func (s *Store) CreateManagementAccountTestTask(ctx context.Context, input port.ManagementAccountTestDispatchCreateInput) (port.ManagementAccountTestTask, bool, error) {
	var row taskRow
	err := s.pool.QueryRow(ctx, managementAccountTestDispatchCreateSQL, input.TaskID, input.Access.ActorSystemAccountID, input.Access.ActorRole, input.Access.FilterSystemAccountID,
		input.AccountID, input.AccountName, input.ProviderCode, input.ProviderProtocolProfileID, input.ProtocolCode, input.ProtocolVersion,
		input.AccountType, input.Diagnostics, input.Model, input.TestEndpointMode, input.DraftAccountEncrypted).Scan(taskArgs(&row)...)
	if err != nil {
		return port.ManagementAccountTestTask{}, false, fmt.Errorf("create account test task: %w", err)
	}
	if strings.TrimSpace(input.SessionID) != "" {
		if _, err := s.pool.Exec(ctx, managementAccountTestDispatchSessionSQL, input.SessionID, input.TaskID); err != nil {
			return port.ManagementAccountTestTask{}, false, fmt.Errorf("bind account test session task: %w", err)
		}
	}
	return row.task(), true, nil
}

func (s *Store) MarkManagementAccountTestEnqueueFailed(ctx context.Context, id string, access port.ManagementAccountTestAccess, message string) (port.ManagementAccountTestTask, bool, error) {
	_, err := s.pool.Exec(ctx, managementAccountTestDispatchFailSQL, strings.TrimSpace(id), access.ActorSystemAccountID, message)
	if err != nil {
		return port.ManagementAccountTestTask{}, false, err
	}
	return s.getTask(ctx, strings.TrimSpace(id), access)
}

var _ port.ManagementAccountTestDispatchStore = (*Store)(nil)
