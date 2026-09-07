import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-usage-options-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-usage-options-demand-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, accountUsageRepository, { statsRouter }, auth, { requestContextMiddleware }] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-usage.repository.js'),
  import('../../modules/stats/stats.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use('/api', auth.requireAuth)
app.use('/api/my-stats', auth.forceSelfAccessScope, statsRouter)
app.use('/api/stats', auth.requireAdmin, statsRouter)
let server: ReturnType<typeof app.listen> | undefined

try {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const owner = repositories.createSystemAccount({
    username: 'usage_options_owner',
    displayName: '用量候选用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const other = repositories.createSystemAccount({
    username: 'usage_options_other',
    displayName: '其他用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const firstId = 'acc_usage_options_first'
  const selectedId = 'acc_usage_options_selected'
  insertAccount(firstId, owner.id, '用量候选 Alpha')
  insertAccount(selectedId, owner.id, '用量候选 Selected')
  insertAccount('acc_usage_options_other', other.id, '用量候选 Other')

  const selfOptions = serialized(accountUsageRepository.listAccountUsageOptions(
    { systemAccountId: owner.id, role: 'user' },
    { keyword: '候选 A', limit: 10, selectedIds: [selectedId] }
  ))
  assert.deepEqual(selfOptions.map((item) => item.id), [selectedId, firstId], '一次请求应按名称包含匹配并合并已选回填和关键词候选')
  const suffixOptions = serialized(accountUsageRepository.listAccountUsageOptions(
    { systemAccountId: owner.id, role: 'user' },
    { keyword: 'Alpha', limit: 10 }
  ))
  assert.deepEqual(suffixOptions.map((item) => item.id), [firstId], '用量候选应支持名称后缀包含搜索')
  assert.deepEqual(Object.keys(selfOptions[0]).sort(), [
    'accessType',
    'id',
    'name',
    'ownerSystemAccountId',
    'ownerSystemAccountName',
    'providerCode',
    'providerName',
    'status',
    'type'
  ], '用户侧用量候选只返回趋势选择消费的字段')
  assert.equal(selfOptions.every((item) => item.ownerSystemAccountId === owner.id), true, '用户侧候选必须固定当前用户作用域')

  const adminOptions = serialized(accountUsageRepository.listAccountUsageOptions(
    { systemAccountId: admin.id, role: 'admin', systemAccountFilterId: owner.id },
    { limit: 10 }
  ))
  assert.equal(adminOptions.length, 2, '管理侧指定用户时只返回该用户账户')
  assert.equal(adminOptions.every((item) => item.systemAccountId === owner.id), true, '管理侧候选应保留系统账户归属')
  assert.equal(adminOptions.every((item) => item.systemAccountName === owner.displayName), true, '管理侧候选应返回可直接显示的用户名称')

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '账户用量候选回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}/api`
  const selfHttpOptions = await getData(baseUrl, `/my-stats/account-usage/options?systemAccountId=${other.id}&keyword=${encodeURIComponent('用量候选')}&selectedIds=${selectedId}`, sessionCookie(owner.id))
  assert.equal(selfHttpOptions.every((item) => item.ownerSystemAccountId === owner.id), true, '用户侧 HTTP 接口必须忽略伪造的 systemAccountId')
  assert.equal(selfHttpOptions.some((item) => item.id === selectedId), true, 'HTTP 候选接口应在一次请求中回填已选账户')
  const adminHttpOptions = await getData(baseUrl, `/stats/account-usage/options?systemAccountId=${owner.id}&limit=10`, sessionCookie(admin.id))
  assert.equal(adminHttpOptions.every((item) => item.systemAccountId === owner.id), true, '管理侧 HTTP 接口必须遵守目标用户作用域')

  for (const option of [...selfOptions, ...adminOptions]) {
    for (const forbiddenField of ['permissions', 'credentials', 'protocolCode', 'providerProtocolProfileId', 'todayUsage', 'usage']) {
      assert.equal(Object.hasOwn(option, forbiddenField), false, `用量候选不得返回 ${forbiddenField}`)
    }
  }

  console.log('账户用量候选按需回归通过：单次请求合并搜索和已选值，响应仅含趋势选择字段，用户作用域不可扩大')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function insertAccount(id: string, systemAccountId: string, name: string): void {
  const now = new Date().toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    INSERT INTO accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credential_mask, credentials_encrypted, concurrency_limit, priority,
      super_priority_enabled, fallback_enabled, schedulable, stream_failure_count,
      health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES (?, ?, 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 'sk-***', '{}', 20, 10, 0, 0, 1, 0, 'gpt-5.5', 'responses_sse', ?, ?)
  `).run(id, systemAccountId, name, now, now)
}

function serialized<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getData(baseUrl: string, path: string, cookie: string): Promise<Array<Record<string, string>>> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  assert.equal(response.ok, true, `${path} HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as { data: Array<Record<string, string>> }).data
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
