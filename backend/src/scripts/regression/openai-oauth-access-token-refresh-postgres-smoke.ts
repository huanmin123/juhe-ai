import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { handleDbServiceOperation } from '../../modules/db-service/db-service-handlers.js'
import * as oauthRefreshService from '../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js'
import { logger } from '../../shared/logger.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  findAccountForTestAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'OpenAI OAuth access token refresh PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

logger.level = 'silent'

const marker = `oauth_refresh_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
const refreshedByToken = new Set<string>()
let oauthProfileId = ''

try {
  oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest(async (operation, persistMode) => {
    assert.equal(persistMode, 'db-service', 'PG smoke 应覆盖 DB service 持久化模式')
    return await handleDbServiceOperation(operation)
  })

  const group = await createGroupAsync({
    name: `OAuth刷新PG烟测分组${marker}`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  createdGroupIds.push(group.id)
  oauthProfileId = GPT_OPENAI_V1_PROFILE_ID

  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
    refreshedByToken.add(refreshToken)
    return {
      accessToken: `access-refreshed-${refreshToken}`,
      refreshToken: `refresh-refreshed-${refreshToken}`,
      expiresIn: 3600,
      expiresAt: new Date(Date.now() + 3600_000).toISOString(),
      clientId: clientId ?? 'pg-smoke-client'
    }
  })

  const dueAccount = await createOAuthAccount('正常刷新账户', 'active-due-token', new Date(Date.now() - 60_000).toISOString(), group.id)
  const freshAccount = await createOAuthAccount('未到期账户', 'fresh-token', new Date(Date.now() + 3600_000).toISOString(), group.id)
  const successResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
    leadSeconds: 300,
    batchSize: 20,
    retryBackoffSeconds: 0,
    persistMode: 'db-service',
    accountIds: [dueAccount.id]
  })

  assert.equal(successResult.scanned, 1, 'PG OAuth 刷新应只扫描到期账户')
  assert.equal(successResult.refreshed, 1, 'PG OAuth 刷新应成功刷新到期账户')
  assert.equal(successResult.failed, 0, 'PG OAuth 成功刷新不应记录失败')
  assert(refreshedByToken.has('active-due-token'), 'PG OAuth 刷新应调用到期账户 refresh token')
  assert(!refreshedByToken.has('fresh-token'), 'PG OAuth 刷新不应调用未到期账户 refresh token')

  const refreshed = await findAccountForTestAsync(dueAccount.id, access)
  assert.equal(refreshed?.credentials.access_token, 'access-refreshed-active-due-token', 'PG OAuth 刷新应写回 access token')
  assert.equal(refreshed?.credentials.refresh_token, 'refresh-refreshed-active-due-token', 'PG OAuth 刷新应写回 refresh token')
  const fresh = await findAccountForTestAsync(freshAccount.id, access)
  assert.equal(fresh?.credentials.refresh_token, 'fresh-token', 'PG OAuth 未到期账户 refresh token 不应变化')

  await assertOAuthRefreshDueExplainUsesIndex()

  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async () => {
    throw new Error('模拟刷新失败 Authorization: Bearer oauth-refresh-bearer-token sk-oauth-refresh-secret-token refresh_token=oauth-refresh-token-secret client_secret=oauth-refresh-client-secret proxy=https://oauth-refresh-proxy-user:oauth-refresh-proxy-password@example.com')
  })
  const failedAccount = await createOAuthAccount('连续失败账户', 'fail-token', new Date(Date.now() - 60_000).toISOString(), group.id, undefined)
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    const failedResult = await oauthRefreshService.refreshDueOpenAIOAuthAccessTokens({
      leadSeconds: 300,
      batchSize: 20,
      retryBackoffSeconds: 0,
      persistMode: 'db-service',
      accountIds: [failedAccount.id]
    })
    assert.equal(failedResult.failed, 1, `第 ${attempt} 次 PG OAuth 刷新应记录 1 个失败`)
    assert.equal(failedResult.exceptioned, attempt === 3 ? 1 : 0, `第 ${attempt} 次 PG OAuth 异常标记数量不正确`)
  }
  const failedLatest = await findAccountForTestAsync(failedAccount.id, access)
  assert.equal(failedLatest?.status, 'error', 'PG OAuth 连续失败 3 次后应标记 error')
  assert.equal(failedLatest?.lastErrorCode, oauthRefreshService.OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE, 'PG OAuth 连续失败应写入固定错误码')
  assertErrorMessageRedacted(failedLatest?.lastErrorMessage)

  console.log(JSON.stringify({
    message: 'OpenAI OAuth access token refresh PG smoke 通过',
    refreshed: successResult.refreshed,
    failureExceptioned: true,
    explainIndexed: true
  }))
} finally {
  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest()
  oauthRefreshService.setOpenAIOAuthDbServiceRequesterForTest()
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function createOAuthAccount(
  name: string,
  refreshToken: string,
  expiresAt: string,
  groupId: string,
  accessToken = `access-${refreshToken}`
): Promise<{ id: string }> {
  const account = await createAccountAsync({
    name: `${name}${marker}`,
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: oauthProfileId,
    type: 'oauth',
    status: 'active',
    schedulable: true,
    groupId,
    credentials: {
      refresh_token: refreshToken,
      access_token: accessToken,
      expires_at: expiresAt,
      client_id: 'pg-smoke-client',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5-mini']
  }, access)
  createdAccountIds.push(account.id)
  return { id: account.id }
}

function assertErrorMessageRedacted(message: string | undefined): void {
  assert(message?.includes('已停止自动刷新'), `PG OAuth 错误信息缺少停止自动刷新说明：${message ?? ''}`)
  for (const secret of [
    'oauth-refresh-bearer-token',
    'sk-oauth-refresh-secret-token',
    'oauth-refresh-token-secret',
    'oauth-refresh-client-secret',
    'oauth-refresh-proxy-user',
    'oauth-refresh-proxy-password'
  ]) {
    assert(!message?.includes(secret), `PG OAuth 错误信息不应包含敏感片段：${secret}`)
  }
}

async function assertOAuthRefreshDueExplainUsesIndex(): Promise<void> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL enable_seqscan = off')
    const planRows = await client.query(`
      EXPLAIN (COSTS OFF)
      SELECT id
      FROM juhe_business.accounts
      WHERE authorization_instance_authorization_id IS NULL
        AND deleted_at IS NULL
        AND provider_protocol_profile_id = $1
        AND type = 'oauth'
        AND oauth_refresh_token_present = 1
        AND (status <> 'error' OR last_error_code IS NULL OR last_error_code <> 'oauth_token_refresh_failed')
        AND (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at <= $2)
      ORDER BY (oauth_access_token_expires_at IS NOT NULL) ASC,
        oauth_access_token_expires_at ASC,
        updated_at ASC,
        id ASC
      LIMIT 20
    `, [oauthProfileId, new Date(Date.now() + 300_000).toISOString()])
    const plan = planRows.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(plan, /idx_accounts_openai_oauth_refresh_pg_due/, 'PG OAuth 刷新候选查询应命中 due 索引')
    assert.doesNotMatch(plan, /\bSeq Scan\b/, 'PG OAuth 刷新候选查询不应出现 Seq Scan')
  } finally {
    await client.query('ROLLBACK').catch(() => undefined)
    client.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const accountIds = [...new Set(createdAccountIds)]
  if (accountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
  }
  const groupIds = [...new Set(createdGroupIds)]
  if (groupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_authorization_settings WHERE group_id = ANY($1::text[])', [groupIds])
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
}
