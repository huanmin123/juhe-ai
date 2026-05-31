import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-storage-sanitizer-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-storage-sanitizer-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })

const secretApiKey = 'sk-model-check-storage-leak-1234567890'
const bearerToken = 'Bearer storage-token-that-must-not-leak'
const proxyUrl = 'https://proxy-user:proxy-password@example.test/v1'
const rawBody = 'RAW-BODY-SHOULD-NOT-LEAK'
const longText = `${'x'.repeat(650)}-tail-should-not-survive`

const [repositories, databaseModule] = await Promise.all([
  import('../../storage/repositories.js'),
  import('../../storage/database.js')
])

const run = repositories.createModelCheckRun({
  systemAccountId: 'sys_admin',
  actorSystemAccountId: 'sys_admin',
  targetType: 'account',
  targetId: 'acc_storage_sanitizer',
  targetName: '存储脱敏检测',
  targetOwnerSystemAccountId: 'sys_admin',
  accountId: 'acc_storage_sanitizer',
  groupId: 'grp_storage_sanitizer',
  model: 'gpt-5.5',
  profile: 'full',
  trustedComparison: false,
  trustedComparisonAvailable: false,
  traceId: 'trace_storage_sanitizer',
  probeSetVersion: 'storage-sanitizer-regression',
  requestSummary: {
    authorization: bearerToken,
    api_key: secretApiKey,
    proxyUrl,
    nested: {
      rawBody,
      harmlessMessage: `错误摘要意外带入 ${secretApiKey} 和 ${proxyUrl}`,
      longText
    },
    manyItems: Array.from({ length: 30 }, (_, index) => `item-${index}`)
  }
})

repositories.createModelCheckItems(run.id, [{
  itemKey: 'target.responses_basic',
  itemType: 'responses_basic',
  status: 'warning',
  score: 6,
  maxScore: 20,
  traceId: 'trace_item_storage_sanitizer',
  evidenceSummary: {
    message: `上游错误：${bearerToken}`,
    access_token: 'access-token-storage-leak',
    full_response: rawBody,
    outputPreview: `模型回显 ${secretApiKey}`
  },
  errorMessage: `代理错误 ${proxyUrl}`
}])

repositories.finishModelCheckRun(run.id, {
  level: 'uncertain',
  score: 42,
  status: 'completed',
  message: `完成但摘要包含 ${secretApiKey}`,
  resultSummary: {
    refresh_token: 'refresh-token-storage-leak',
    message: `结果摘要包含 ${bearerToken}`
  }
})

const detail = repositories.getModelCheckRunDetail(run.id, { systemAccountId: 'sys_admin', role: 'admin' })
assert(detail, '模型检测详情应可读取')

const serialized = JSON.stringify(detail)
assert(!serialized.includes(secretApiKey), '模型检测记录不得泄露 OpenAI API Key 形态字符串')
assert(!serialized.includes('storage-token-that-must-not-leak'), '模型检测记录不得泄露 Bearer token')
assert(!serialized.includes('proxy-password'), '模型检测记录不得泄露代理密码')
assert(!serialized.includes(rawBody), '模型检测记录不得保存 raw body / full response')
assert(!serialized.includes('tail-should-not-survive'), '模型检测长字符串摘要应被截断')
assert(serialized.includes('[redacted]'), '敏感字段或敏感字符串应显示为已脱敏')

const manyItems = detail.requestSummary.manyItems
assert(Array.isArray(manyItems), '数组摘要仍应保留结构')
assert(manyItems.length <= 20, '数组摘要应限制长度')

const list = repositories.listModelCheckRuns({ systemAccountId: 'sys_admin', role: 'admin' }, { page: 1, pageSize: 10 })
assert.equal(list.items.length, 1, '检测历史列表应可分页读取模型检测摘要')
assert(!JSON.stringify(list).includes(secretApiKey), '检测历史列表同样不得泄露敏感字符串')

const atomicRun = repositories.createModelCheckRun({
  systemAccountId: 'sys_admin',
  actorSystemAccountId: 'sys_admin',
  targetType: 'account',
  targetId: 'acc_storage_atomic',
  targetName: '检测项原子写入',
  targetOwnerSystemAccountId: 'sys_admin',
  accountId: 'acc_storage_atomic',
  groupId: 'grp_storage_atomic',
  model: 'gpt-5.5',
  profile: 'full',
  trustedComparison: false,
  trustedComparisonAvailable: false,
  traceId: 'trace_storage_atomic',
  probeSetVersion: 'storage-sanitizer-regression',
  requestSummary: {}
})
assert.throws(() => {
  repositories.createModelCheckItems(atomicRun.id, [
    {
      id: 'mci_storage_atomic_duplicate',
      itemKey: 'target.atomic_first',
      itemType: 'atomic',
      status: 'passed',
      score: 1,
      maxScore: 1
    },
    {
      id: 'mci_storage_atomic_duplicate',
      itemKey: 'target.atomic_second',
      itemType: 'atomic',
      status: 'failed',
      score: 0,
      maxScore: 1
    }
  ])
}, /constraint|UNIQUE/i, '检测项批量写入中途失败时应抛出数据库约束错误')
const atomicDetail = repositories.getModelCheckRunDetail(atomicRun.id, { systemAccountId: 'sys_admin', role: 'admin' })
assert.equal(atomicDetail?.checks.length, 0, '检测项批量写入必须原子回滚，不能留下半批历史残留')

const otherRun = repositories.createModelCheckRun({
  systemAccountId: 'sys_other',
  actorSystemAccountId: 'sys_other',
  targetType: 'account',
  targetId: 'acc_storage_other',
  targetName: '范围隔离检测',
  targetOwnerSystemAccountId: 'sys_other',
  accountId: 'acc_storage_other',
  groupId: 'grp_storage_other',
  model: 'gpt-5.4',
  profile: 'full',
  trustedComparison: false,
  trustedComparisonAvailable: false,
  traceId: 'trace_storage_other',
  probeSetVersion: 'storage-sanitizer-regression',
  requestSummary: {}
})
repositories.finishModelCheckRun(otherRun.id, {
  level: 'likely',
  score: 88,
  status: 'completed',
  message: '其他系统账户模型检测已完成',
  resultSummary: {}
})

const datasetDatabase = databaseModule.getDatasetDatabase()
const originalPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
datasetDatabase.prepare = ((sql: string) => {
  const statement = originalPrepare(sql)
  if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+model_check_runs\s+mcr\b/i.test(sql)) {
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.all = ((...params: SQLInputValue[]) => {
      capturedCalls.push({ sql, params })
      return originalAll(...params)
    }) as typeof statement.all
  }
  return statement
}) as typeof datasetDatabase.prepare

try {
  const scopedList = repositories.listModelCheckRuns({ systemAccountId: 'sys_admin', role: 'user' }, {
    page: 1,
    pageSize: 10,
    status: 'completed',
    model: 'gpt-5.5'
  })
  assert(scopedList.items.every((item) => item.systemAccountId === undefined), '普通用户侧模型检测历史不应暴露系统账户字段')
  assert(scopedList.items.some((item) => item.id === run.id), '模型检测历史列表应包含当前系统账户的记录')
  assert(!scopedList.items.some((item) => item.id === otherRun.id), '模型检测历史列表不应混入其他系统账户记录')
} finally {
  datasetDatabase.prepare = originalPrepare
}

assert(capturedCalls.length >= 1, '应捕获模型检测历史列表 SQL')
for (const call of capturedCalls) {
  assert(/\bmcr\.system_account_id\s+=\s+\?/i.test(call.sql), '模型检测历史列表应下推系统账户作用域')
  assert(/\bmcr\.status\s+=\s+\?/i.test(call.sql), '模型检测历史列表应下推状态筛选')
  assert(/\bmcr\.model\s+=\s+\?/i.test(call.sql), '模型检测历史列表应下推模型筛选')
  assert(/\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(call.sql), '模型检测历史列表必须分页查询')
  assert(!/\bLIKE\s+\?/i.test(call.sql), '模型检测历史列表不应使用 LIKE 扫描')
  assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '模型检测历史列表不应传入前导通配符参数')
}
for (const indexName of [
  'idx_model_check_runs_system_account_created',
  'idx_model_check_runs_system_account_model_created',
  'idx_model_check_runs_system_account_level_created',
  'idx_model_check_runs_system_account_status_created',
  'idx_model_check_runs_system_account_target_created',
  'idx_model_check_items_run_order',
  'idx_model_check_items_run_key',
  'idx_model_check_items_run_status'
]) {
  assertRecordIndexExists(indexName)
}

console.log('模型检测存储脱敏回归通过：报告摘要不会泄露 API Key、token、代理密码或原始体')

function assertRecordIndexExists(indexName: string): void {
  const row = databaseModule.getDatasetDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `数据集目录库应创建索引 ${indexName}`)
}
