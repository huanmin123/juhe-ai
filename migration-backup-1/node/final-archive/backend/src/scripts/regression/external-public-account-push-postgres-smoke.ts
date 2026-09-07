import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  AccountConfigRevisionConflictError,
  findAccountSummaryAsync,
  updateAccountAsync
} from '../../storage/repositories.js'
import {
  addPublicApiKeyAsync,
  addPublicGroupAsync,
  addPublicWelfareAccountAsync,
  deletePublicApiKeyAsync,
  deletePublicGroupAsync,
  deletePublicWelfareAccountAsync,
  listPublicApiKeysAsync,
  listPublicGroupsAsync,
  listPublicWelfareAccountsAsync,
  updatePublicApiKeyAsync,
  updatePublicGroupAsync,
  updatePublicWelfareAccountAsync
} from '../../modules/external-integrations/external-public-account-push.service.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '公开账号推送 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `pubpush_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const targetUsername = marker.slice(0, 70)
const targetDisplayName = `公开推送PGsmoke${marker}`
const accountName = `公开推送 PG smoke 账号 ${marker}`
const updatedAccountName = `公开推送 PG smoke 账号更新 ${marker}`
const initialGroupName = `公开推送 PG smoke 初始分组 ${marker}`
const extraGroupName = `公开推送 PG smoke 分组 ${marker}`
const updatedGroupName = `公开推送 PG smoke 分组更新 ${marker}`
const apiKeyName = `公开推送 PG smoke Key ${marker}`
const updatedApiKeyName = `公开推送 PG smoke Key 更新 ${marker}`

let targetSystemAccountId: string | undefined
let accountId: string | undefined
let extraGroupId: string | undefined
let apiKeyId: string | undefined
let routeStrategyId: string | undefined

try {
  await cleanupSmokeRows()

  const createdAccount = await addPublicWelfareAccountAsync({
    targetUsername,
    targetDisplayName,
    targetGroupName: initialGroupName,
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: accountName,
    type: 'api_key',
    baseUrl: 'https://api.openai.com/v1',
    apiKey: `sk-public-push-pg-smoke-${marker}`,
    supportedModels: ['gpt-4o-mini'],
    status: 'disabled',
    concurrencyLimit: 3,
    priority: 7
  })
  targetSystemAccountId = createdAccount.target.systemAccountId
  accountId = createdAccount.account.id
  assert.equal(createdAccount.action, 'created', 'PG 公开账号新增应返回 created')
  assert.equal(createdAccount.target.username, targetUsername, 'PG 公开账号新增应自动创建目标用户')
  assert.equal(createdAccount.target.groupName, initialGroupName, 'PG 公开账号新增应自动创建目标分组')
  assert.equal(Object.prototype.hasOwnProperty.call(createdAccount.account, 'credentials'), false, 'PG 公开账号新增响应不能返回凭据')

  const listedAccounts = await listPublicWelfareAccountsAsync({
    targetUsername,
    keyword: accountName,
    page: 1,
    pageSize: 20
  })
  assert.ok(listedAccounts.items.some((item) => item.id === accountId), 'PG 公开账号列表应返回新增账号')
  assert.equal(Object.prototype.hasOwnProperty.call(listedAccounts.items[0] ?? {}, 'credentials'), false, 'PG 公开账号列表不能返回凭据')

  const updatedAccount = await updatePublicWelfareAccountAsync({
    targetUsername,
    accountId,
    name: updatedAccountName,
    status: 'active',
    supportedModels: ['gpt-4o-mini'],
    concurrencyLimit: 5,
    priority: 9
  })
  assert.equal(updatedAccount.action, 'updated', 'PG 公开账号应可修改')
  assert.equal(updatedAccount.account.name, updatedAccountName, 'PG 公开账号修改应返回新名称')
  assert.equal(updatedAccount.account.status, 'active', 'PG 公开账号修改应更新状态')

  const ownerAccess = {
    systemAccountId: createdAccount.target.systemAccountId,
    role: 'user' as const
  }
  const staleAccount = await findAccountSummaryAsync(createdAccount.account.id, ownerAccess)
  assert(staleAccount, 'PG revision smoke 必须读取公开账号 owner summary')
  assert.equal(typeof staleAccount.configRevision, 'number', 'PG revision smoke 必须读取 stale config revision')

  const winnerNotes = `PG revision winner ${marker}`
  const winner = await updateAccountAsync(createdAccount.account.id, { notes: winnerNotes }, ownerAccess)
  assert(winner, 'PG revision smoke 必须先提交 winner notes')
  assert.equal(winner.notes, winnerNotes, 'PG revision winner 应写入 notes')
  assert.equal(winner.configRevision, (staleAccount.configRevision ?? 0) + 1, 'PG revision winner 应递增配置版本')
  const winnerCredentials = structuredClone(winner.credentials)
  const winnerModels = [...(winner.supportedModels ?? [])]

  await assert.rejects(
    updateAccountAsync(createdAccount.account.id, {
      notes: `PG stale revision 不应写入 ${marker}`
    }, ownerAccess, {
      expectedConfigRevision: staleAccount.configRevision
    }),
    (error: unknown) => error instanceof AccountConfigRevisionConflictError
      && error.expectedConfigRevision === staleAccount.configRevision,
    'PG updateAccountAsync 必须拒绝 stale expected revision'
  )

  const afterStaleRejection = await findAccountSummaryAsync(createdAccount.account.id, ownerAccess)
  assert(afterStaleRejection, 'PG stale revision 拒绝后账号应仍存在')
  assert.equal(afterStaleRejection.notes, winnerNotes, 'PG stale revision 不得覆盖 winner notes')
  assert.equal(afterStaleRejection.configRevision, winner.configRevision, 'PG stale revision 不得递增 winner revision')
  assert.deepEqual(afterStaleRejection.credentials, winnerCredentials, 'PG stale revision 不得覆盖 winner credentials')
  assert.deepEqual(afterStaleRejection.supportedModels ?? [], winnerModels, 'PG stale revision 不得覆盖 winner models')

  const createdGroup = await addPublicGroupAsync({
    targetUsername,
    name: extraGroupName,
    providerCode: GPT_VENDOR_CODE,
    enabled: true,
    groupType: 'personal'
  })
  assert.equal(createdGroup.action, 'created', 'PG 公开分组新增应返回 created')
  assert.ok(createdGroup.group?.id, 'PG 公开分组新增应返回分组 ID')
  extraGroupId = createdGroup.group?.id

  const listedGroups = await listPublicGroupsAsync({
    targetUsername,
    keyword: extraGroupName,
    page: 1,
    pageSize: 20
  })
  assert.ok(listedGroups.items.some((item) => item.id === extraGroupId), 'PG 公开分组列表应返回新增分组')

  const updatedGroup = await updatePublicGroupAsync({
    targetUsername,
    groupId: extraGroupId,
    name: updatedGroupName,
    enabled: false
  })
  assert.equal(updatedGroup.action, 'updated', 'PG 公开分组应可修改')
  assert.equal(updatedGroup.group?.name, updatedGroupName, 'PG 公开分组修改应返回新名称')
  assert.equal(updatedGroup.group?.enabled, false, 'PG 公开分组修改应更新启用状态')

  routeStrategyId = await createSmokeRouteStrategy(targetSystemAccountId, extraGroupId)
  const createdApiKey = await addPublicApiKeyAsync({
    targetUsername,
    name: apiKeyName,
    routeStrategyId,
    status: 'active'
  })
  assert.equal(createdApiKey.action, 'created', 'PG 公开 API Key 新增应返回 created')
  assert.ok(createdApiKey.apiKey?.id, 'PG 公开 API Key 新增应返回 API Key ID')
  assert.ok(createdApiKey.apiKey?.key, 'PG 公开 API Key 新增应只在创建响应返回完整密钥')
  apiKeyId = createdApiKey.apiKey?.id

  const listedApiKeys = await listPublicApiKeysAsync({
    targetUsername,
    keyword: apiKeyName,
    status: 'all',
    page: 1,
    pageSize: 20
  })
  assert.ok(listedApiKeys.items.some((item) => item.id === apiKeyId), 'PG 公开 API Key 列表应返回新增 Key')
  assert.equal(listedApiKeys.items.find((item) => item.id === apiKeyId)?.key, undefined, 'PG 公开 API Key 列表不能返回完整密钥')

  const updatedApiKey = await updatePublicApiKeyAsync({
    targetUsername,
    apiKeyId,
    name: updatedApiKeyName,
    status: 'disabled',
    routeStrategyId
  })
  assert.equal(updatedApiKey.action, 'updated', 'PG 公开 API Key 应可修改')
  assert.equal(updatedApiKey.apiKey?.name, updatedApiKeyName, 'PG 公开 API Key 修改应返回新名称')
  assert.equal(updatedApiKey.apiKey?.status, 'disabled', 'PG 公开 API Key 修改应更新状态')

  const deletedApiKey = await deletePublicApiKeyAsync({ targetUsername, apiKeyId })
  assert.equal(deletedApiKey.action, 'deleted', 'PG 公开 API Key 应可删除')
  apiKeyId = undefined

  await deleteSmokeRouteStrategy(routeStrategyId)
  routeStrategyId = undefined

  const deletedGroup = await deletePublicGroupAsync({ targetUsername, groupId: extraGroupId })
  assert.equal(deletedGroup.action, 'deleted', 'PG 公开分组应可删除')
  extraGroupId = undefined

  const deletedAccount = await deletePublicWelfareAccountAsync({ targetUsername, accountId })
  assert.equal(deletedAccount.action, 'deleted', 'PG 公开账号应可删除')
  accountId = undefined

  console.log(JSON.stringify({
    message: '公开账号推送 PG smoke 通过',
    targetUsername,
    targetSystemAccountId,
    accountCreated: true,
    groupCreated: true,
    apiKeyCreated: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function createSmokeRouteStrategy(systemAccountId: string, groupId: string): Promise<string> {
  const pool = await getPostgresPool()
  const id = `route_strategy_public_push_${marker}`
  const bindingId = `rsg_public_push_${marker}`
  const now = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_business.route_strategies (
      id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
    ) VALUES ($1, $2, $3, NULL, 'normal', 'active', 0, NULL, $4, $4)
  `, [id, systemAccountId, `公开推送 PG smoke 策略 ${marker}`, now])
  await pool.query(`
    INSERT INTO juhe_business.route_strategy_groups (
      id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
    ) VALUES ($1, $2, $3, $4, 1, 1, 'active', $5, $5)
  `, [bindingId, id, systemAccountId, groupId, now])
  return id
}

async function deleteSmokeRouteStrategy(id: string | undefined): Promise<void> {
  if (!id) {
    return
  }
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id = $1', [id])
  await pool.query('DELETE FROM juhe_business.route_strategies WHERE id = $1', [id])
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const accounts = await pool.query(`
    SELECT id
    FROM juhe_business.system_accounts
    WHERE username = $1
  `, [targetUsername])
  const systemAccountIds = (accounts.rows as Array<{ id: string }>).map((row) => row.id)
  if (systemAccountIds.length === 0) {
    return
  }

  const apiKeys = await pool.query(`
    SELECT id
    FROM juhe_business.api_keys
    WHERE system_account_id = ANY($1::text[])
  `, [systemAccountIds])
  const apiKeyIds = (apiKeys.rows as Array<{ id: string }>).map((row) => row.id)
  const ownedAccounts = await pool.query(`
    SELECT id
    FROM juhe_business.accounts
    WHERE system_account_id = ANY($1::text[])
  `, [systemAccountIds])
  const ownedAccountIds = (ownedAccounts.rows as Array<{ id: string }>).map((row) => row.id)

  await pool.query('DELETE FROM juhe_business.api_key_schedule_status_events WHERE api_key_id = ANY($1::text[])', [apiKeyIds])
  await pool.query('DELETE FROM juhe_business.api_keys WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.group_accounts WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.account_schedule_status_events WHERE account_id = ANY($1::text[])', [ownedAccountIds])
  await pool.query('DELETE FROM juhe_business.accounts WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.route_strategies WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.groups WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.system_settings WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.system_sessions WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])', [systemAccountIds])
}
