import { strict as assert } from 'node:assert'
import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { captchaAnswerForTest } from '../../modules/auth/captcha.service.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账户单独绑定分组 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

interface ApiEnvelope<T> {
  data: T
}

interface GroupResponse {
  id: string
  name: string
  providerCode: string
}

interface AccountResponse {
  id: string
  name: string
  status: string
  boundGroupId?: string
}

const marker = `account_single_group_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const groupAName = `单绑PG烟测分组A${marker}`
const groupBName = `单绑PG烟测分组B${marker}`
const accountName = `单绑PG烟测账号${marker}`
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
let server: http.Server | undefined

try {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', trustProxy: true })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
  const cookie = await login(baseUrl)

  const groupA = await postEnvelope<GroupResponse>(baseUrl, '/__aisys__/api/groups', {
    name: groupAName,
    providerCode: 'gpt',
    enabled: true,
    groupType: 'high_concurrency'
  }, cookie, 201)
  createdGroupIds.push(groupA.id)
  const groupB = await postEnvelope<GroupResponse>(baseUrl, '/__aisys__/api/groups', {
    name: groupBName,
    providerCode: 'gpt',
    enabled: true,
    groupType: 'high_concurrency'
  }, cookie, 201)
  createdGroupIds.push(groupB.id)

  const createdAccount = await postEnvelope<AccountResponse>(baseUrl, '/__aisys__/api/accounts', {
    name: accountName,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'temporary_unavailable',
    groupId: groupA.id,
    credentials: {
      api_key: `sk-${marker}`,
      base_url: 'https://example.invalid/v1'
    },
    supportedModels: ['gpt-5-mini'],
    healthCheckModel: 'gpt-5-mini'
  }, cookie, 201)
  createdAccountIds.push(createdAccount.id)
  assert.equal(createdAccount.boundGroupId, groupA.id, 'PG smoke 新建账户应先绑定 A 分组')

  const rebound = await postEnvelope<AccountResponse>(
    baseUrl,
    `/__aisys__/api/accounts/${createdAccount.id}/group`,
    { groupId: groupB.id },
    cookie
  )
  assert.equal(rebound.id, createdAccount.id, '单独绑定分组响应应返回原账户')
  assert.equal(rebound.boundGroupId, groupB.id, '单独绑定分组响应应返回 B 分组')

  const binding = await readAccountBinding(createdAccount.id)
  assert.equal(binding.account?.name, accountName, 'PG 应能查回 smoke 账户')
  assert.equal(binding.bindingCount, 1, 'PG 单独绑定分组后账户应只保留一个绑定')
  assert.equal(binding.boundGroupId, groupB.id, 'PG 单独绑定分组后应切到 B 分组')
  assert.equal(binding.groupABindingCount, 0, 'PG 单独绑定分组后 A 分组绑定应移除')
  assert.equal(binding.groupBBindingCount, 1, 'PG 单独绑定分组后 B 分组绑定应存在')

  console.log(JSON.stringify({
    message: '账户单独绑定分组 HTTP PG smoke 通过',
    accountId: createdAccount.id,
    fromGroupId: groupA.id,
    toGroupId: groupB.id,
    bindingCount: binding.bindingCount
  }))
} finally {
  if (server) {
    await closeServer(server).catch(() => undefined)
  }
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function login(baseUrl: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert(captchaCode, '测试夹具应能读取验证码答案')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    signal: AbortSignal.timeout(10_000),
    body: JSON.stringify({
      username: 'admin',
      password: 'admin',
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `账户单独绑定分组 PG smoke 登录应成功，实际 HTTP ${response.status}: ${text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(cookie, '账户单独绑定分组 PG smoke 登录应返回 session cookie')
  return cookie
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: cookie ? { cookie } : undefined,
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GET ${path} 应返回 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function postEnvelope<T>(
  baseUrl: string,
  path: string,
  body: unknown,
  cookie: string,
  expectedStatus = 200
): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      cookie
    },
    signal: AbortSignal.timeout(15_000),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `POST ${path} 应返回 ${expectedStatus}，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function readAccountBinding(accountId: string): Promise<{
  account?: { id: string; name: string }
  boundGroupId?: string
  bindingCount: number
  groupABindingCount: number
  groupBBindingCount: number
}> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT
      accounts.id,
      accounts.name,
      MIN(group_accounts.group_id) AS bound_group_id,
      COUNT(group_accounts.group_id) AS binding_count,
      COUNT(*) FILTER (WHERE group_accounts.group_id = $2) AS group_a_binding_count,
      COUNT(*) FILTER (WHERE group_accounts.group_id = $3) AS group_b_binding_count
    FROM juhe_business.accounts
    LEFT JOIN juhe_business.group_accounts ON group_accounts.account_id = accounts.id
    WHERE accounts.id = $1
    GROUP BY accounts.id, accounts.name
    LIMIT 1
  `, [accountId, createdGroupIds[0], createdGroupIds[1]])
  const row = result.rows[0] as Record<string, unknown> | undefined
  if (!row) {
    return {
      bindingCount: 0,
      groupABindingCount: 0,
      groupBBindingCount: 0
    }
  }
  return {
    account: {
      id: String(row.id),
      name: String(row.name)
    },
    boundGroupId: typeof row.bound_group_id === 'string' ? row.bound_group_id : undefined,
    bindingCount: Number(row.binding_count ?? 0),
    groupABindingCount: Number(row.group_a_binding_count ?? 0),
    groupBBindingCount: Number(row.group_b_binding_count ?? 0)
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
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
  }
  const groupIds = [...new Set(createdGroupIds)]
  if (groupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [groupIds])
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
  await pool.query('DELETE FROM juhe_business.groups WHERE name IN ($1, $2)', [groupAName, groupBName])
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.once('listening', () => resolve())
  })
}

function closeServer(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) reject(error)
      else resolve()
    })
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address === 'object', '测试 HTTP 服务应监听到本地端口')
  return address
}
