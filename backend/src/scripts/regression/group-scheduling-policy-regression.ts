import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  GPT_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-scheduling-policy-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'group-scheduling-policy.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'group-scheduling-policy-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { groupsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/groups/groups.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-groups', forceSelfAccessScope, groupsRouter)
app.use('/__aisys__/api/groups', requireAdmin, groupsRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface GroupSummaryResponse {
  id: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  groupType: string
  schedulingPolicy?: {
    defaultSoftConcurrency?: number
    maxQueueWaitMs?: number
    clientIpConcurrencyLimit?: number
    clientIpConcurrencyOverflowMode?: 'reject' | 'queue'
    imageLaneMaxConcurrency?: number
  }
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const database = databaseModule.getBusinessDatabase()
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const adminCookie = sessionCookie(admin.id)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('分组调度策略回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`

  assertCurrentColumns('groups', ['group_type', 'scheduling_policy_json'])
  assertCurrentColumns('group_accounts', [
    'system_account_id',
    'group_id',
    'account_id',
    'account_authorization_id',
    'local_priority',
    'local_super_priority_enabled',
    'local_fallback_enabled',
    'enabled',
    'created_at',
    'updated_at'
  ])
  assertCurrentColumns('group_authorization_settings', [
    'authorization_id',
    'system_account_id',
    'group_id',
    'enabled',
    'group_type',
    'scheduling_policy_json',
    'created_at',
    'updated_at'
  ])

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const routeCreatedGroup = await postEnvelope<GroupSummaryResponse>(baseUrl, '/__aisys__/api/groups', adminCookie, {
    name: '路由协议档案字段回归分组',
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 2,
      maxQueueWaitMs: 30000,
      clientIpConcurrencyLimit: 0,
      clientIpConcurrencyOverflowMode: 'reject',
      imageLaneMaxConcurrency: 0
    }
  })
  assert.equal(routeCreatedGroup.providerProtocolProfileId, GPT_OPENAI_V1_PROFILE_ID, '分组创建路由应接受并返回 providerProtocolProfileId')
  assert.equal(routeCreatedGroup.schedulingPolicy?.defaultSoftConcurrency, 2, '分组创建路由应保留高并发调度策略')

  assert.throws(
    () => repositories.createGroup({
      name: '高并发调度策略旧字段回归分组',
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      groupType: 'high_concurrency',
      schedulingPolicy: {
        defaultSoftConcurrency: 3,
        fastFirstEnabled: false
      }
    }, access),
    /分组调度策略包含未知字段/,
    '分组调度策略不应再静默丢弃旧字段或不可配置字段'
  )

  const highConcurrencyGroup = repositories.createGroup({
    name: '高并发调度策略回归分组',
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 3,
      maxQueueWaitMs: 45000,
      clientIpConcurrencyLimit: 4,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 0
    }
  }, access)

  assert.equal(highConcurrencyGroup.groupType, 'high_concurrency')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.defaultSoftConcurrency, 3)
  assert.equal(highConcurrencyGroup.schedulingPolicy?.fastFirstEnabled, true, '高并发快速优先应默认开启，不允许分组配置关闭')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.breakAffinityOnSoftLimit, true, '达到阈值打破亲和应默认开启，不允许分组配置关闭')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.maxQueueWaitMs, 45000, '短队列最大等待时间应允许按分组配置')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.maxQueueSize, 1000, '分组队列上限应使用内置默认值，不允许被请求体调小')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.perApiKeyQueueLimit, 1000, '单 Key 队列上限默认应跟随分组队列上限')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.clientIpConcurrencyLimit, 4, '单 IP 并发上限应允许按分组配置')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.clientIpConcurrencyOverflowMode, 'queue', '单 IP 超限模式应允许切换为排队等待')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.imageLaneMaxConcurrency, 0, '图像通道上限 0 应保留为自动预留文本槽策略')

  const stored = database
    .prepare('SELECT group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(highConcurrencyGroup.id) as unknown as { group_type: string; scheduling_policy_json: string | null }
  const storedPolicy = JSON.parse(stored.scheduling_policy_json ?? '{}') as Record<string, unknown>
  assert.equal(stored.group_type, 'high_concurrency')
  assert.equal(storedPolicy.defaultSoftConcurrency, 3, '高并发策略应写入 JSON 配置')
  assert.equal(storedPolicy.maxQueueWaitMs, 45000, '最大等待时间应按请求值写入')
  assert.equal(storedPolicy.maxQueueSize, 1000, '队列上限应按内置默认值写入')
  assert.equal(storedPolicy.perApiKeyQueueLimit, 1000, '单 Key 队列上限应跟随分组队列上限写入')
  assert.equal(storedPolicy.fastFirstEnabled, true, '默认开启策略不应按请求关闭')
  assert.equal(storedPolicy.clientIpConcurrencyLimit, 4, '单 IP 并发上限应写入 JSON 配置')
  assert.equal(storedPolicy.clientIpConcurrencyOverflowMode, 'queue', '单 IP 超限模式应写入 JSON 配置')
  assert.equal(storedPolicy.imageLaneMaxConcurrency, 0, '图像通道上限 0 应按自动策略写入 JSON 配置')
  assert(stored.scheduling_policy_json, '高并发分组应写入完整调度策略 JSON')

  const routeUpdatedGroup = await patchEnvelope<GroupSummaryResponse>(baseUrl, `/__aisys__/api/groups/${highConcurrencyGroup.id}`, adminCookie, {
    name: highConcurrencyGroup.name,
    providerCode: highConcurrencyGroup.providerCode,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    description: highConcurrencyGroup.description ?? '',
    enabled: highConcurrencyGroup.enabled,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 4,
      maxQueueWaitMs: 600000,
      clientIpConcurrencyLimit: 0,
      clientIpConcurrencyOverflowMode: 'reject',
      imageLaneMaxConcurrency: 0
    }
  })
  assert.equal(routeUpdatedGroup.providerProtocolProfileId, GPT_OPENAI_V1_PROFILE_ID, '分组更新路由应接受前端保存时提交的 providerProtocolProfileId')
  assert.equal(routeUpdatedGroup.schedulingPolicy?.defaultSoftConcurrency, 4, '分组更新路由应保留前端提交的高并发调度策略')

  database
    .prepare('UPDATE groups SET scheduling_policy_json = NULL WHERE id = ?')
    .run(highConcurrencyGroup.id)
  assert.throws(
    () => repositories.findGroupSummary(highConcurrencyGroup.id, access),
    /高并发分组调度策略缺失/,
    '读取已存储高并发分组时不应把缺失策略静默补成默认值'
  )
  database
    .prepare('UPDATE groups SET scheduling_policy_json = ? WHERE id = ?')
    .run(JSON.stringify({ defaultSoftConcurrency: 3 }), highConcurrencyGroup.id)
  assert.throws(
    () => repositories.findGroupSummary(highConcurrencyGroup.id, access),
    /分组调度策略缺少字段/,
    '读取已存储高并发分组时不应把缺字段策略静默补成默认值'
  )
  database
    .prepare('UPDATE groups SET scheduling_policy_json = ? WHERE id = ?')
    .run(stored.scheduling_policy_json, highConcurrencyGroup.id)

  const runtimeAccess = repositories.resolveGroupUsageAccessMetadata(highConcurrencyGroup.id, 'sys_admin')
  assert.equal(runtimeAccess?.groupType, 'high_concurrency')
  assert.equal(runtimeAccess?.schedulingPolicy?.defaultSoftConcurrency, 3)
  assert.equal(runtimeAccess?.schedulingPolicy?.maxQueueWaitMs, 45000)
  assert.equal(runtimeAccess?.schedulingPolicy?.maxQueueSize, 1000)
  assert.equal(runtimeAccess?.schedulingPolicy?.perApiKeyQueueLimit, 1000)
  assert.equal(runtimeAccess?.schedulingPolicy?.clientIpConcurrencyLimit, 4)
  assert.equal(runtimeAccess?.schedulingPolicy?.clientIpConcurrencyOverflowMode, 'queue')
  assert.equal(runtimeAccess?.schedulingPolicy?.imageLaneMaxConcurrency, 0)

  const options = repositories.listGroupOptions(access, { keyword: highConcurrencyGroup.name })
  assert.equal(options[0]?.groupType, 'high_concurrency', '分组选项应携带分组类型')

  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '高并发绑定单账户排队阈值账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-group-scheduling-policy',
      base_url: 'https://api.openai.com/v1'
    },
    concurrencyLimit: 10,
    groupId: highConcurrencyGroup.id
  }, access)
  const boundAccount = repositories.setAccountGroup(account.id, highConcurrencyGroup.id, access)
  assert.equal(boundAccount?.boundGroupId, highConcurrencyGroup.id, '账户绑定应只记录目标分组')
  const binding = database
    .prepare('SELECT group_id, account_id FROM group_accounts WHERE group_id = ? AND account_id = ?')
    .get(highConcurrencyGroup.id, account.id) as unknown as { group_id?: string; account_id?: string } | undefined
  assert.equal(binding?.group_id, highConcurrencyGroup.id, '账户绑定不应依赖绑定级调度参数')
  const mismatchedProfileGroup = createSyntheticGptProfileMismatchGroup(access)
  const movedToSameProviderGroup = repositories.setAccountGroup(account.id, mismatchedProfileGroup.id, access)
  assert.equal(movedToSameProviderGroup?.boundGroupId, mismatchedProfileGroup.id, '账户绑定分组只按供应商校验，协议档案由账户类型决定')
  const mismatchedBinding = database
    .prepare('SELECT group_id FROM group_accounts WHERE account_id = ? AND system_account_id = ?')
    .get(account.id, access.systemAccountId) as unknown as { group_id?: string } | undefined
  assert.equal(mismatchedBinding?.group_id, mismatchedProfileGroup.id, '同供应商分组应允许承接同一账户')

  const personalGroup = repositories.updateGroup(highConcurrencyGroup.id, { groupType: 'personal' }, access)
  assert.equal(personalGroup?.groupType, 'personal')
  assert.equal(personalGroup?.schedulingPolicy, undefined)
  const personalStored = database
    .prepare('SELECT group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(highConcurrencyGroup.id) as unknown as { group_type: string; scheduling_policy_json: string | null }
  assert.equal(personalStored.group_type, 'personal')
  assert.equal(personalStored.scheduling_policy_json, null)

  console.log('分组调度策略回归通过：schema、创建/更新、选项和运行态元数据均携带高并发分组配置')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function tableColumns(tableName: string): string[] {
  return (databaseModule.getBusinessDatabase().prepare(`PRAGMA table_info(${tableName})`).all() as Array<{ name?: string }>)
    .map((column) => column.name)
    .filter((name): name is string => Boolean(name))
}

function assertCurrentColumns(tableName: string, expectedColumns: string[]): void {
  const columns = tableColumns(tableName)
  for (const column of expectedColumns) {
    assert(columns.includes(column), `${tableName} 应包含当前字段 ${column}`)
  }
}

function createSyntheticGptProfileMismatchGroup(access: { systemAccountId: string; role: 'admin' }) {
  const now = new Date().toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO provider_protocol_profiles (
        id, provider_code, name, description, enabled, protocol_code, protocol_version,
        base_url, default_test_model, account_types_json, capabilities_json, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      'profile_gpt_profile_mismatch_regression',
      'gpt',
      '回归专用 GPT 非默认协议档案',
      '仅用于验证账号绑定不再按 providerProtocolProfileId 隔离',
      ANTHROPIC_PROTOCOL_CODE,
      ANTHROPIC_PROTOCOL_VERSION,
      'https://example.invalid/v1',
      'gpt-5.5',
      JSON.stringify(['api_key']),
      JSON.stringify([]),
      now,
      now
    )
  return repositories.createGroup({
    name: 'profile 不匹配回归分组',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_profile_mismatch_regression',
    enabled: true
  }, access)
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  return requestEnvelope<T>(baseUrl, path, cookie, 'POST', body)
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  return requestEnvelope<T>(baseUrl, path, cookie, 'PATCH', body)
}

async function requestEnvelope<T>(baseUrl: string, path: string, cookie: string, method: 'POST' | 'PATCH', body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
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
    const timeout = setTimeout(() => {
      listeningServer.closeAllConnections?.()
      resolvePromise()
    }, 1000)
    listeningServer.close((error) => {
      clearTimeout(timeout)
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
  })
}
