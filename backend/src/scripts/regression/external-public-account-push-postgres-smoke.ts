import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  addPublicApiKeyAsync,
  addPublicGroup,
  addPublicWelfareAccount,
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

try {
  await cleanupSmokeRows()

  const createdAccount = await addPublicWelfareAccount({
    targetUsername,
    targetDisplayName,
    targetGroupName: initialGroupName,
    providerCode: 'gpt',
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

  const createdGroup = await addPublicGroup({
    targetUsername,
    name: extraGroupName,
    providerCode: 'gpt',
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

  const routeStrategyId = await loadDefaultRouteStrategyId(targetSystemAccountId)
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

async function loadDefaultRouteStrategyId(systemAccountId: string): Promise<string> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT id
    FROM juhe_business.route_strategies
    WHERE system_account_id = $1
      AND is_default = 1
    ORDER BY created_at DESC, id ASC
    LIMIT 1
  `, [systemAccountId])
  const rows = result.rows as Array<{ id?: string }>
  const id = rows[0]?.id
  assert.ok(id, 'PG 公开 API Key 新增前应能找到目标用户默认路由策略')
  return id
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
