import { strict as assert } from 'node:assert'
import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { captchaAnswerForTest } from '../../modules/auth/captcha.service.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账户导入 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

interface ApiEnvelope<T> {
  data: T
}

interface ImportResult {
  mode: 'preview' | 'import'
  canImport: boolean
  imported: boolean
  summary: {
    accounts: { total: number; create: number; skip: number; failed: number }
    groups: { create: number; reuse: number; failed: number }
  }
  accounts: Array<{ action: string; accountId?: string; messages: string[] }>
}

const marker = `account_import_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const groupName = `导入PG烟测分组${marker}`
const accountName = `导入PG烟测账号${marker}`
const schedule = {
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5], start: '22:00', end: '23:55' }
  ]
}
const importPayload = {
  data: {
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [
      {
        name: accountName,
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupName,
        supportedModels: ['gpt-5.5'],
        tags: ['PG导入烟测'],
        availabilitySchedule: schedule,
        credentials: {
          api_key: `sk-${marker}`,
          base_url: 'https://api.openai.com/v1'
        }
      }
    ]
  },
  options: {
    createMissingGroups: true,
    createMissingProxies: true,
    skipDuplicates: true
  }
}

const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
let server: http.Server | undefined

try {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', trustProxy: true })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

  const cookie = await login(baseUrl)
  const preview = await postEnvelope<ImportResult>(baseUrl, '/__aisys__/api/accounts/import/preview', importPayload, cookie)
  assert.equal(preview.mode, 'preview', '账户导入 preview 应返回 preview 模式')
  assert.equal(preview.canImport, true, `账户导入 PG preview 应可导入：${JSON.stringify(preview)}`)
  assert.equal(preview.summary.accounts.create, 1, '账户导入 PG preview 应计划创建 1 个账户')
  assert.equal(preview.summary.groups.create, 1, '账户导入 PG preview 应计划创建 1 个分组')

  const imported = await postEnvelope<ImportResult>(baseUrl, '/__aisys__/api/accounts/import/confirm', importPayload, cookie)
  assert.equal(imported.mode, 'import', '账户导入 confirm 应返回 import 模式')
  assert.equal(imported.imported, true, `账户导入 PG confirm 应成功：${JSON.stringify(imported)}`)
  assert.equal(imported.summary.accounts.create, 1, '账户导入 PG confirm 应创建 1 个账户')
  assert.equal(imported.summary.groups.create, 1, '账户导入 PG confirm 应创建 1 个分组')
  const accountId = imported.accounts[0]?.accountId
  assert(accountId, '账户导入 PG confirm 应返回新账户 ID')
  createdAccountIds.push(accountId)

  const smokeRows = await readImportedRows(accountId)
  assert.equal(smokeRows.account?.name, accountName, 'PG 导入账户名称应落库')
  assert.equal(smokeRows.account?.status, 'pending_test', 'active 导入账户应先进入 pending_test')
  assert.match(String(smokeRows.account?.availability_schedule_json ?? ''), /22:00/, 'PG 导入账户应保存 availabilitySchedule JSON')
  assert.equal(smokeRows.group?.name, groupName, 'PG 导入自动创建分组应落库')
  assert.equal(smokeRows.bindingCount, 1, 'PG 导入账户应绑定到自动创建分组')
  assert.equal(smokeRows.tagCount, 1, 'PG 导入账户标签应落库')
  if (smokeRows.group?.id) {
    createdGroupIds.push(smokeRows.group.id)
  }

  console.log(JSON.stringify({
    message: '账户导入 HTTP PG smoke 通过',
    accountId,
    groupId: smokeRows.group?.id,
    previewCreate: preview.summary.accounts.create,
    imported: imported.imported,
    bindingCount: smokeRows.bindingCount,
    tagCount: smokeRows.tagCount
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
  assert.equal(response.status, 200, `账户导入 PG smoke 登录应成功，实际 HTTP ${response.status}: ${text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(cookie, '账户导入 PG smoke 登录应返回 session cookie')
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

async function postEnvelope<T>(baseUrl: string, path: string, body: unknown, cookie: string): Promise<T> {
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
  assert.equal(response.status, 200, `POST ${path} 应返回 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function readImportedRows(accountId: string): Promise<{
  account?: Record<string, unknown>
  group?: { id: string; name: string }
  bindingCount: number
  tagCount: number
}> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT
      accounts.id,
      accounts.name,
      accounts.status,
      accounts.availability_schedule_json,
      groups.id AS group_id,
      groups.name AS group_name,
      (SELECT COUNT(*) FROM juhe_business.group_accounts WHERE account_id = accounts.id AND group_id = groups.id) AS binding_count,
      (SELECT COUNT(*) FROM juhe_business.account_tag_bindings WHERE account_id = accounts.id) AS tag_count
    FROM juhe_business.accounts
    LEFT JOIN juhe_business.group_accounts ON group_accounts.account_id = accounts.id
    LEFT JOIN juhe_business.groups ON groups.id = group_accounts.group_id
    WHERE accounts.id = $1
    LIMIT 1
  `, [accountId])
  const row = result.rows[0] as Record<string, unknown> | undefined
  if (!row) {
    return { bindingCount: 0, tagCount: 0 }
  }
  return {
    account: row,
    group: typeof row.group_id === 'string' && typeof row.group_name === 'string'
      ? { id: row.group_id, name: row.group_name }
      : undefined,
    bindingCount: Number(row.binding_count ?? 0),
    tagCount: Number(row.tag_count ?? 0)
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
  await pool.query("DELETE FROM juhe_business.account_tags WHERE name = 'PG导入烟测' AND NOT EXISTS (SELECT 1 FROM juhe_business.account_tag_bindings WHERE account_tag_bindings.tag_id = account_tags.id)")
  const groupIds = [...new Set(createdGroupIds)]
  if (groupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [groupIds])
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
  await pool.query('DELETE FROM juhe_business.groups WHERE name = $1', [groupName])
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
