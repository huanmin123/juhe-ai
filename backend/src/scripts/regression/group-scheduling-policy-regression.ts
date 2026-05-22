import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-scheduling-policy-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'group-scheduling-policy.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'group-scheduling-policy-records.sqlite3')
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
  const database = databaseModule.getDatabase()
  assert(tableColumns('groups').includes('group_type'), 'groups 应包含 group_type 字段')
  assert(tableColumns('groups').includes('scheduling_policy_json'), 'groups 应包含 scheduling_policy_json 字段')
  assert(tableColumns('group_accounts').includes('soft_concurrency_limit'), 'group_accounts 应包含 soft_concurrency_limit 字段')

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const highConcurrencyGroup = repositories.createGroup({
    name: '高并发调度策略回归分组',
    providerCode: 'openai',
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 3,
      maxQueueWaitMs: 2000,
      breakAffinityOnSoftLimit: false,
      fastFirstEnabled: false
    }
  }, access)

  assert.equal(highConcurrencyGroup.groupType, 'high_concurrency')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.defaultSoftConcurrency, 3)
  assert.equal(highConcurrencyGroup.schedulingPolicy?.weightAffectsSoftConcurrency, true)
  assert.equal(highConcurrencyGroup.schedulingPolicy?.fastFirstEnabled, true, '高并发快速优先应默认开启，不允许分组配置关闭')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.breakAffinityOnSoftLimit, true, '达到阈值打破亲和应默认开启，不允许分组配置关闭')
  assert.equal(highConcurrencyGroup.schedulingPolicy?.maxQueueWaitMs, 3000, '短队列等待阈值应使用内置默认值，不暴露为分组配置')

  const stored = database
    .prepare('SELECT group_type, scheduling_policy_json FROM groups WHERE id = ?')
    .get(highConcurrencyGroup.id) as unknown as { group_type: string; scheduling_policy_json: string | null }
  assert.equal(stored.group_type, 'high_concurrency')
  assert(stored.scheduling_policy_json?.includes('"defaultSoftConcurrency":3'), '高并发策略应写入 JSON 配置')
  assert(!stored.scheduling_policy_json?.includes('"maxQueueWaitMs":2000'), '非用户配置项不应按请求值写入')
  assert(!stored.scheduling_policy_json?.includes('"fastFirstEnabled":false'), '默认开启策略不应按请求关闭')

  const runtimeAccess = repositories.resolveGroupUsageAccessMetadata(highConcurrencyGroup.id, 'sys_admin')
  assert.equal(runtimeAccess?.groupType, 'high_concurrency')
  assert.equal(runtimeAccess?.schedulingPolicy?.defaultSoftConcurrency, 3)

  const options = repositories.listGroupOptions(access, { keyword: highConcurrencyGroup.name })
  assert.equal(options[0]?.groupType, 'high_concurrency', '分组选项应携带分组类型')

  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '高并发绑定单账户排队阈值账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-group-scheduling-policy',
      base_url: 'http://127.0.0.1:9/v1'
    },
    concurrencyLimit: 10
  }, access)
  const boundAccount = repositories.setAccountGroup(account.id, highConcurrencyGroup.id, access, {
    dispatchWeight: 3,
    softConcurrencyLimit: 2
  })
  assert.equal(boundAccount?.boundGroupDispatchWeight, 3, '账户绑定应返回绑定级权重')
  assert.equal(boundAccount?.boundGroupSoftConcurrencyLimit, 2, '账户绑定应返回绑定级单账户排队阈值')
  const binding = database
    .prepare('SELECT weight, soft_concurrency_limit FROM group_accounts WHERE group_id = ? AND account_id = ?')
    .get(highConcurrencyGroup.id, account.id) as unknown as { weight?: number; soft_concurrency_limit?: number } | undefined
  assert.equal(binding?.weight, 3, '账户绑定权重应写入 group_accounts')
  assert.equal(binding?.soft_concurrency_limit, 2, '账户绑定单账户排队阈值应写入 group_accounts')

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
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function tableColumns(tableName: string): string[] {
  return (databaseModule.getDatabase().prepare(`PRAGMA table_info(${tableName})`).all() as Array<{ name?: string }>)
    .map((column) => column.name)
    .filter((name): name is string => Boolean(name))
}
