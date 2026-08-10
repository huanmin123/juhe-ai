import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import type { AccountListItem } from '../../domain/types.js'
import { runAccountListAvailabilityProjectionMaintenanceInClient } from '../../modules/accounts/account-list-availability-projection.service.js'
import {
  AccountListAvailabilityProjectionUnavailableError,
  listAccountListAvailabilityProjectionPageInClient,
  markAccountListAvailabilityDirtyInClient
} from '../../storage/account-list-availability-projection.repository.js'
import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { applyBusinessSchema } from '../../storage/schema/business-schema.js'

const database = new DatabaseSync(':memory:')
applyBusinessSchema(database)
const client = createSqliteDatabaseClient(database)
const viewerSystemAccountId = 'projection_maintenance_viewer'
const activeAccountId = 'projection_maintenance_active'
const deletedAccountId = 'projection_maintenance_deleted'
const failedAccountId = 'projection_maintenance_failed'

try {
  insertFixture()
  await verifyBatchProjectsVisibleRowsAndDeletesInvisibleRows()
  await verifyReadFailureLeavesTheProjectionUnavailable()
  console.log('account list availability projection maintenance regression passed')
} finally {
  database.close()
}

async function verifyBatchProjectsVisibleRowsAndDeletesInvisibleRows(): Promise<void> {
  await markAccountListAvailabilityDirtyInClient(client, {
    accountId: activeAccountId,
    reason: 'account_changed',
    nowMs: 1_000
  })
  await markAccountListAvailabilityDirtyInClient(client, {
    accountId: deletedAccountId,
    reason: 'authorization_revoked',
    nowMs: 1_000
  })
  database.prepare(`UPDATE resource_authorizations SET status = 'revoked' WHERE id = 'projection_maintenance_revoked_authorization'`).run()

  const result = await runAccountListAvailabilityProjectionMaintenanceInClient(client, {
    ownerId: 'projection-maintenance-regression',
    batchSize: 10,
    leaseMs: 1_000,
    now: new Date(1_000),
    loadItems: async (viewerId, ids) => {
      assert.equal(viewerId, viewerSystemAccountId)
      assert.deepEqual(ids, [activeAccountId])
      return [projectionItem(activeAccountId)]
    }
  })
  assert.equal(result.claimed, 2)
  assert.equal(result.projected, 1)
  assert.equal(result.deleted, 1)
  assert.equal(result.released, 0)

  const page = await listAccountListAvailabilityProjectionPageInClient(client, {
    viewerSystemAccountId,
    maximumProjectionAgeMs: 10_000,
    nowMs: 1_000,
    options: { status: 'active', page: 1, pageSize: 20 }
  })
  assert.deepEqual(page.items.map((item) => item.id), [activeAccountId])
}

async function verifyReadFailureLeavesTheProjectionUnavailable(): Promise<void> {
  insertAccount(failedAccountId)
  await markAccountListAvailabilityDirtyInClient(client, {
    accountId: failedAccountId,
    reason: 'runtime_state_changed',
    nowMs: 2_000
  })
  const result = await runAccountListAvailabilityProjectionMaintenanceInClient(client, {
    ownerId: 'projection-maintenance-regression',
    batchSize: 10,
    leaseMs: 1_000,
    now: new Date(2_000),
    loadItems: async () => {
      throw new Error('simulated_runtime_read_failure')
    }
  })
  assert.equal(result.projected, 0)
  assert.equal(result.released, 1, '后台读取失败必须显式释放为重放，而非确认旧行')
  await assert.rejects(
    () => listAccountListAvailabilityProjectionPageInClient(client, {
      viewerSystemAccountId,
      maximumProjectionAgeMs: 10_000,
      nowMs: 2_000,
      options: { page: 1, pageSize: 20 }
    }),
    AccountListAvailabilityProjectionUnavailableError,
    '失败中的 dirty 行必须让读取明确 unavailable'
  )
}

function insertFixture(): void {
  const now = new Date(0).toISOString()
  database.prepare(`
    INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
    VALUES (?, ?, ?, 'user', 'active', 'test-only', ?, ?)
  `).run(viewerSystemAccountId, viewerSystemAccountId, 'Projection maintenance viewer', now, now)
  database.prepare(`INSERT INTO providers (id, code, name, created_at, updated_at) VALUES ('gpt', 'gpt', 'GPT', ?, ?)`)
    .run(now, now)
  database.prepare(`INSERT INTO protocols (id, code, version, name, created_at, updated_at) VALUES ('openai_v1', 'openai', 'v1', 'OpenAI v1', ?, ?)`)
    .run(now, now)
  database.prepare(`
    INSERT INTO provider_protocol_profiles (
      id, provider_code, name, protocol_code, protocol_version, base_url,
      default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
    ) VALUES ('projection_maintenance_profile', 'gpt', 'Projection maintenance', 'openai', 'v1',
      'https://example.invalid/v1', 'gpt-5-mini', '["api_key"]', '[]', ?, ?)
  `).run(now, now)
  insertAccount(activeAccountId)
  insertAccount(deletedAccountId)
  database.prepare(`
    INSERT INTO resource_authorizations (
      id, resource_type, resource_id, resource_owner_system_account_id,
      grantee_system_account_id, scope, status, created_by, created_at, updated_at
    ) VALUES ('projection_maintenance_revoked_authorization', 'account', ?, ?, ?, 'use', 'active', ?, ?, ?)
  `).run(activeAccountId, viewerSystemAccountId, viewerSystemAccountId, viewerSystemAccountId, now, now)
  database.prepare(`
    UPDATE accounts
    SET authorization_instance_source_account_id = ?,
        authorization_instance_authorization_id = ?
    WHERE id = ?
  `).run(activeAccountId, 'projection_maintenance_revoked_authorization', deletedAccountId)
}

function insertAccount(accountId: string): void {
  const now = new Date(0).toISOString()
  database.prepare(`
    INSERT INTO accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, credentials_encrypted,
      health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES (?, ?, 'gpt', 'projection_maintenance_profile', 'openai', 'v1', ?, 'api_key', 'active', '{}',
      'gpt-5-mini', 'chat_json', ?, ?)
  `).run(accountId, viewerSystemAccountId, accountId, now, now)
}

function projectionItem(id: string): AccountListItem {
  return {
    id,
    configRevision: 1,
    ownerSystemAccountId: viewerSystemAccountId,
    providerCode: 'gpt',
    providerName: 'GPT',
    providerProtocolProfileId: 'projection_maintenance_profile',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: id,
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 10,
    currentConcurrency: 0,
    effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' },
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    tags: [],
    healthCheckModel: 'gpt-5-mini',
    healthCheckEndpointMode: 'chat_json',
    schedulable: true,
    todayUsage: { requestCount: 0, totalTokens: 0, totalCost: 0 },
    accessType: 'owner',
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canReturnAuthorization: false,
      canAuthorize: true,
      canViewCredentials: true
    }
  }
}
