import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import type { SQLInputValue } from 'node:sqlite'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
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
  groupType: string
  schedulingPolicy?: {
    defaultSoftConcurrency?: number
    maxQueueWaitMs?: number
    clientIpConcurrencyLimit?: number
    clientIpConcurrencyOverflowMode?: 'reject' | 'queue'
    imageLaneMaxConcurrency?: number
  }
}

interface GroupCreateListItemResponse {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  ownerSystemAccountId?: string
  name: string
  providerCode: string
  groupType: string
  updatedAt: string
  accountStats: Record<string, number>
  canEdit: boolean
  canDelete: boolean
  canReturn: boolean
}

interface GroupMutationResponse {
  id: string
  changedFields: string[]
  updatedAt: string
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
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const createProviderReads: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+\"?providers\"?\b/i.test(sql) && /^\s*SELECT\b/i.test(sql)) {
      const originalGet = statement.get.bind(statement) as typeof statement.get
      statement.get = ((...params: SQLInputValue[]) => {
        createProviderReads.push({ sql, params })
        return originalGet(...params)
      }) as typeof statement.get
    }
    return statement
  }) as typeof database.prepare
  let routeCreatedGroup: GroupCreateListItemResponse
  try {
    routeCreatedGroup = await postEnvelope<GroupCreateListItemResponse>(baseUrl, '/__aisys__/api/groups', adminCookie, {
      name: '路由分组字段回归分组',
      providerCode: 'gpt',
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
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(createProviderReads.length, 1, '分组创建只能按目标 providerCode 查询一次供应商')
  assert.match(createProviderReads[0]?.sql ?? '', /SELECT\s+id,\s*code,\s*name,\s*enabled[\s\S]*WHERE\s+code\s*=\s*\?[\s\S]*LIMIT\s+1/i, '分组创建必须使用单供应商窄投影')
  assert.deepEqual(createProviderReads[0]?.params, ['gpt'], '分组创建供应商校验只能携带当前 providerCode')
  assert.doesNotMatch(createProviderReads[0]?.sql ?? '', /provider_protocol_profiles|default_supported_models_json|description/i, '分组创建不得装配协议档案、默认模型或说明')
  assert.equal('providerProtocolProfileId' in routeCreatedGroup, false, '分组创建路由不应返回 providerProtocolProfileId')
  assert.equal('schedulingPolicy' in routeCreatedGroup, false, '分组创建回执不得提前返回编辑专用调度策略')
  assert.equal('accountIds' in routeCreatedGroup, false, '分组创建回执不得返回账户关系')
  assert.equal('usage' in routeCreatedGroup.accountStats, false, '分组创建回执不得返回列表不消费的累计用量')
  assert.equal('todayUsage' in routeCreatedGroup.accountStats, false, '空分组创建回执不得返回无意义的今日用量对象')
  assert.deepEqual(Object.keys(routeCreatedGroup.accountStats).sort(), [
    'active',
    'available',
    'concurrencyLimit',
    'currentConcurrency',
    'disabled',
    'error',
    'rateLimited',
    'total'
  ], '分组创建回执只返回列表首屏直接消费的空统计字段')
  assert.equal(routeCreatedGroup.canEdit, true, '新建自有分组应直接返回列表操作权限')
  assert.equal(routeCreatedGroup.canDelete, true, '新建自有分组应直接返回列表删除权限')
  assert.equal(routeCreatedGroup.canReturn, false, '新建自有分组不应返回授权归还权限')
  assert.equal(routeCreatedGroup.systemAccountId, admin.id, '管理端创建回执应直接返回当前列表作用域的系统账户 ID')
  assert.equal(routeCreatedGroup.ownerSystemAccountId, admin.id, '创建回执应直接返回资源所有者 ID')
  assert.ok(routeCreatedGroup.systemAccountName, '管理端创建回执应直接返回可显示的系统账户名称')
  assert.match(routeCreatedGroup.updatedAt, /^\d{4}-\d{2}-\d{2}T/, '分组创建回执应返回可用于编辑版本与列表排序的更新时间')
  const routeCreatedGroupEdit = await getEnvelope<GroupSummaryResponse>(baseUrl, `/__aisys__/api/groups/${routeCreatedGroup.id}/edit-basic`, adminCookie)
  assert.equal(routeCreatedGroupEdit.schedulingPolicy?.defaultSoftConcurrency, 2, '高并发调度策略只在用户打开编辑时按需读取')

  assert.throws(
    () => repositories.createGroup({
      name: '高并发调度策略旧字段回归分组',
      providerCode: 'gpt',
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
  assert.equal(highConcurrencyGroup.schedulingPolicy?.maxQueueSize, runtimeConfig.concurrency.globalMax, '分组队列上限未配置时应跟随全局共享容量')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.perApiKeyQueueLimit, runtimeConfig.concurrency.globalMax, '单 Key 队列上限未配置时应跟随全局共享容量')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.clientIpConcurrencyLimit, 4, '单 IP 并发上限应允许按分组配置')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.clientIpConcurrencyOverflowMode, 'queue', '单 IP 超限模式应允许切换为排队等待')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.imageLaneMaxConcurrency, 0, '图像通道上限 0 应表示与文本共用账户总并发')

  const stored = database
    .prepare('SELECT group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(highConcurrencyGroup.id) as unknown as { group_type: string; scheduling_policy_json: string | null }
  const storedPolicy = JSON.parse(stored.scheduling_policy_json ?? '{}') as Record<string, unknown>
  assert.equal(stored.group_type, 'high_concurrency')
  assert.equal(storedPolicy.defaultSoftConcurrency, 3, '高并发策略应写入 JSON 配置')
  assert.equal(storedPolicy.maxQueueWaitMs, 45000, '最大等待时间应按请求值写入')
  assert.equal(storedPolicy.maxQueueSize, runtimeConfig.concurrency.globalMax, '队列上限应按全局共享容量写入')
  assert.equal(storedPolicy.perApiKeyQueueLimit, runtimeConfig.concurrency.globalMax, '单 Key 队列上限应跟随全局共享容量写入')
  assert.equal(storedPolicy.fastFirstEnabled, true, '默认开启策略不应按请求关闭')
  assert.equal(storedPolicy.clientIpConcurrencyLimit, 4, '单 IP 并发上限应写入 JSON 配置')
  assert.equal(storedPolicy.clientIpConcurrencyOverflowMode, 'queue', '单 IP 超限模式应写入 JSON 配置')
  assert.equal(storedPolicy.imageLaneMaxConcurrency, 0, '图像通道上限 0 应按共用账户并发策略写入 JSON 配置')
  assert(stored.scheduling_policy_json, '高并发分组应写入完整调度策略 JSON')

  const missingVersionResponse = await fetch(`${baseUrl}/__aisys__/api/groups/${highConcurrencyGroup.id}`, {
    method: 'PATCH',
    headers: { cookie: adminCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: '缺少版本不得保存' })
  })
  assert.equal(missingVersionResponse.status, 400, '分组 PATCH 必须强制要求客户端版本')
  const initialHttpVersion = groupUpdatedAt(highConcurrencyGroup.id)
  const routeUpdatedGroup = await patchEnvelope<GroupMutationResponse>(baseUrl, `/__aisys__/api/groups/${highConcurrencyGroup.id}`, adminCookie, {
    expectedUpdatedAt: initialHttpVersion,
    schedulingPolicy: {
      defaultSoftConcurrency: 4,
      maxQueueWaitMs: 600000,
      clientIpConcurrencyLimit: 0,
      clientIpConcurrencyOverflowMode: 'reject',
      imageLaneMaxConcurrency: 0
    }
  })
  assert.deepEqual(Object.keys(routeUpdatedGroup).sort(), ['changedFields', 'id', 'updatedAt'], '分组 PATCH 只能返回写入结果与新版本，不得回传完整摘要')
  assert.equal(routeUpdatedGroup.id, highConcurrencyGroup.id)
  assert.deepEqual(routeUpdatedGroup.changedFields, ['schedulingPolicy'], '分组 PATCH 应只报告实际变化字段')
  assert.equal(repositories.findGroupSummary(highConcurrencyGroup.id, access)?.schedulingPolicy?.defaultSoftConcurrency, 4, '分组更新路由应保存前端提交的高并发调度策略')
  const staleResponse = await fetch(`${baseUrl}/__aisys__/api/groups/${highConcurrencyGroup.id}`, {
    method: 'PATCH',
    headers: { cookie: adminCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: '过期版本不得覆盖', expectedUpdatedAt: initialHttpVersion })
  })
  assert.equal(staleResponse.status, 409, '过期分组版本必须返回 HTTP 409')
  assert.equal(groupUpdatedAt(highConcurrencyGroup.id), routeUpdatedGroup.updatedAt, '过期 PATCH 不得推进版本')
  const routeNoopGroup = await patchEnvelope<GroupMutationResponse>(baseUrl, `/__aisys__/api/groups/${highConcurrencyGroup.id}`, adminCookie, {
    expectedUpdatedAt: routeUpdatedGroup.updatedAt,
    schedulingPolicy: {
      defaultSoftConcurrency: 4,
      maxQueueWaitMs: 600000,
      clientIpConcurrencyLimit: 0,
      clientIpConcurrencyOverflowMode: 'reject',
      imageLaneMaxConcurrency: 0
    }
  })
  assert.deepEqual(routeNoopGroup, { id: highConcurrencyGroup.id, changedFields: [], updatedAt: routeUpdatedGroup.updatedAt }, '分组同值 PATCH 必须返回未推进的当前版本，不得回传完整摘要')
  database.prepare("UPDATE providers SET enabled = 0 WHERE code = 'gpt'").run()
  try {
    const sameDisabledProviderNoop = await patchEnvelope<GroupMutationResponse>(baseUrl, `/__aisys__/api/groups/${highConcurrencyGroup.id}`, adminCookie, {
      expectedUpdatedAt: routeNoopGroup.updatedAt,
      providerCode: 'gpt'
    })
    assert.deepEqual(sameDisabledProviderNoop, { id: highConcurrencyGroup.id, changedFields: [], updatedAt: routeNoopGroup.updatedAt }, '同值供应商 PATCH 不得因当前供应商已停用而触发校验')
  } finally {
    database.prepare("UPDATE providers SET enabled = 1 WHERE code = 'gpt'").run()
  }

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
  assert.equal(runtimeAccess?.schedulingPolicy?.maxQueueSize, runtimeConfig.concurrency.globalMax)
  assert.equal(runtimeAccess?.schedulingPolicy?.perApiKeyQueueLimit, runtimeConfig.concurrency.globalMax)
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
    supportedModels: ['gpt-5.1'],
    healthCheckModel: 'gpt-5.1',
    concurrencyLimit: 10,
    groupId: highConcurrencyGroup.id
  }, access)
  const boundAccount = repositories.setAccountGroup(account.id, highConcurrencyGroup.id, access)
  assert.equal(boundAccount?.boundGroupId, highConcurrencyGroup.id, '账户绑定应只记录目标分组')
  const binding = database
    .prepare('SELECT group_id, account_id FROM group_accounts WHERE group_id = ? AND account_id = ?')
    .get(highConcurrencyGroup.id, account.id) as unknown as { group_id?: string; account_id?: string } | undefined
  assert.equal(binding?.group_id, highConcurrencyGroup.id, '账户绑定不应依赖绑定级调度参数')
  const sameProviderGroup = createSameProviderGroup(access)
  const movedToSameProviderGroup = repositories.setAccountGroup(account.id, sameProviderGroup.id, access)
  assert.equal(movedToSameProviderGroup?.boundGroupId, sameProviderGroup.id, '账户绑定分组只按供应商校验，协议档案由账户类型决定')
  const mismatchedBinding = database
    .prepare('SELECT group_id FROM group_accounts WHERE account_id = ? AND system_account_id = ?')
    .get(account.id, access.systemAccountId) as unknown as { group_id?: string } | undefined
  assert.equal(mismatchedBinding?.group_id, sameProviderGroup.id, '同供应商分组应允许承接同一账户')

  const personalGroup = repositories.updateGroup(highConcurrencyGroup.id, { groupType: 'personal' }, access)
  assert.equal(personalGroup?.groupType, 'personal')
  assert.equal(personalGroup?.schedulingPolicy, undefined)
  const personalStored = database
    .prepare('SELECT group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(highConcurrencyGroup.id) as unknown as { group_type: string; scheduling_policy_json: string | null }
  assert.equal(personalStored.group_type, 'personal')
  assert.equal(personalStored.scheduling_policy_json, null)

  console.log('分组调度策略回归通过：创建返回窄列表行，编辑、选项和运行态按需读取高并发配置')
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

function groupUpdatedAt(groupId: string): string {
  const row = databaseModule.getBusinessDatabase().prepare('SELECT updated_at FROM groups WHERE id = ?').get(groupId) as unknown as { updated_at?: string } | undefined
  assert(row?.updated_at, '分组版本不存在')
  return row.updated_at
}

function assertCurrentColumns(tableName: string, expectedColumns: string[]): void {
  const columns = tableColumns(tableName)
  for (const column of expectedColumns) {
    assert(columns.includes(column), `${tableName} 应包含当前字段 ${column}`)
  }
}

function createSameProviderGroup(access: { systemAccountId: string; role: 'admin' }) {
  return repositories.createGroup({
    name: '同供应商绑定回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  return requestEnvelope<T>(baseUrl, path, cookie, 'POST', body)
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const payload = await response.json() as ApiEnvelope<T>
  assert.equal(response.status, 200, payload.message ?? `${path} 请求失败`)
  return payload.data
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
