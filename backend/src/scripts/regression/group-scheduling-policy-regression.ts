import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
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
  databaseModule,
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const database = databaseModule.getBusinessDatabase()
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
  assert.throws(
    () => repositories.createGroup({
      name: '高并发调度策略旧字段回归分组',
      providerCode: 'openai',
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
    providerCode: 'openai',
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
    providerCode: 'openai',
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
