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
const emptyViewerSystemAccountId = 'projection_maintenance_empty_viewer'
const drainAccountPrefix = 'projection_maintenance_drain_'

try {
  insertFixture()
  await verifyBatchProjectsVisibleRowsAndDeletesInvisibleRows()
  await verifyExistingEmptyViewerHealthIsBackfilled()
  await verifyMaintenanceDrainsMultipleBoundedBatches()
  await verifyReadFailureLeavesTheProjectionUnavailable()
  console.log('account list availability projection maintenance regression passed')
} finally {
  database.close()
}

async function verifyExistingEmptyViewerHealthIsBackfilled(): Promise<void> {
  const now = new Date(1_500).toISOString()
  database.prepare(`
    INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
    VALUES (?, ?, 'Projection empty viewer', 'user', 'active', 'test-only', ?, ?)
  `).run(emptyViewerSystemAccountId, emptyViewerSystemAccountId, now, now)
  const result = await runAccountListAvailabilityProjectionMaintenanceInClient(client, {
    ownerId: 'projection-maintenance-empty-viewer',
    batchSize: 10,
    maxBatchesPerRun: 1,
    now: new Date(1_500),
    loadItems: async () => {
      throw new Error('空账户 viewer 不应调用旧列表水合')
    }
  })
  assert.equal(result.viewerHealthBootstrapped, 1, '既有空账户 viewer 必须补齐 health 行')
  const page = await listAccountListAvailabilityProjectionPageInClient(client, {
    viewerSystemAccountId: emptyViewerSystemAccountId,
    nowMs: 1_500,
    options: { page: 1, pageSize: 20 }
  })
  assert.deepEqual(page.items, [], '无账户的已存在 viewer 完成 health 回填后必须返回 200 空页')
}

async function verifyMaintenanceDrainsMultipleBoundedBatches(): Promise<void> {
  const accountIds = Array.from({ length: 25 }, (_, index) => `${drainAccountPrefix}${String(index + 1).padStart(2, '0')}`)
  for (const accountId of accountIds) {
    insertAccount(accountId)
    await markAccountListAvailabilityDirtyInClient(client, {
      accountId,
      reason: 'bulk_bootstrap',
      nowMs: 1_750
    })
  }
  const observedBatchSizes: number[] = []
  const result = await runAccountListAvailabilityProjectionMaintenanceInClient(client, {
    ownerId: 'projection-maintenance-drain',
    batchSize: 10,
    maxBatchesPerRun: 3,
    now: new Date(1_750),
    loadItems: async (viewerId, ids) => {
      assert.equal(viewerId, viewerSystemAccountId)
      assert(ids.length <= 10, '单个 legacy hydration 批次不得突破既有 100 账户边界')
      observedBatchSizes.push(ids.length)
      return ids.map((id) => projectionItem(id))
    }
  })
  assert.equal(result.claimed, accountIds.length, '受限 drain 必须在本轮处理所有可领取的三批账户')
  assert.equal(result.projected, accountIds.length)
  assert.deepEqual(observedBatchSizes, [10, 10, 5], '大范围 bootstrap 必须拆为固定上限的批次')
  const remaining = database.prepare(`
    SELECT COUNT(*) AS count
    FROM account_list_availability_dirty
    WHERE account_id LIKE ?
  `).get(`${drainAccountPrefix}%`) as { count: number }
  assert.equal(remaining.count, 0, 'drain 完成后不能留下已可处理的脏行')
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
  const remainingDirty = database.prepare(`
    SELECT account_id, generation, claim_token
    FROM account_list_availability_dirty
    WHERE viewer_system_account_id = ?
  `).all(viewerSystemAccountId)
  const health = database.prepare(`
    SELECT is_current, projection_count, next_transition_at
    FROM account_list_availability_projection_viewer_health
    WHERE viewer_system_account_id = ?
  `).get(viewerSystemAccountId) as { is_current: number; projection_count: number; next_transition_at: string | null } | undefined
  assert.deepEqual(remainingDirty, [], `物化后不能残留 viewer 脏行：${JSON.stringify(remainingDirty)}`)
  assert.equal(health?.is_current, 1, `物化后 viewer health 必须可读：${JSON.stringify(health)}`)
  assert(
    !health?.next_transition_at || Date.parse(health.next_transition_at) > 1_000,
    `未到期的测试投影不能将 viewer health 标成不可读：${JSON.stringify(health)}`
  )

  const page = await listAccountListAvailabilityProjectionPageInClient(client, {
    viewerSystemAccountId,
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
