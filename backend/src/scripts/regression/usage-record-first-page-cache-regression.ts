import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  isUsageRecordFirstPageOptions,
  usageRecordFirstPageCandidateInputs,
  usageRecordFirstPageSummaryFromInput,
  type UsageRecordFirstPageNameMaps
} from '../../modules/usage-records/usage-record-first-page-cache.service.js'

assert.equal(isUsageRecordFirstPageOptions({ page: 1, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'gateway' }), true)
assert.equal(isUsageRecordFirstPageOptions({ page: 2, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'gateway' }), false)
assert.equal(isUsageRecordFirstPageOptions({ page: 1, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'manual_account_test' }), false)
assert.equal(isUsageRecordFirstPageOptions({ page: 1, pageSize: 20, sortBy: 'createdAt', sortOrder: 'desc', trafficSource: 'gateway', model: 'gpt-5' }), false)

const names: UsageRecordFirstPageNameMaps = {
  apiKeyNames: new Map([['key-1', '主要 Key']]),
  groupNames: new Map([['group-1', '默认 GPT 分组']]),
  accountNames: new Map([['account-1', '主账号']])
}
const summary = usageRecordFirstPageSummaryFromInput({
  id: 'usage-1',
  systemAccountId: 'system-1',
  traceId: 'trace-1',
  trafficSource: 'gateway',
  apiKeyId: 'key-1',
  groupId: 'group-1',
  accountId: 'account-1',
  success: true,
  createdAt: '2026-07-18T10:13:05.078Z'
}, names)
assert.equal(summary.apiKeyName, '主要 Key', '首屏热列表必须缓存 API Key 名称')
assert.equal(summary.groupName, '默认 GPT 分组', '首屏热列表必须缓存分组名称')
assert.equal(summary.accountName, '主账号', '首屏热列表必须缓存 AI 账户名称')

const candidates = usageRecordFirstPageCandidateInputs([
  {
    id: 'usage-gateway',
    systemAccountId: 'system-1',
    traceId: 'trace-gateway',
    trafficSource: 'gateway',
    success: true,
    createdAt: '2026-07-18T10:13:05.078Z'
  },
  {
    id: 'usage-manual',
    systemAccountId: 'system-1',
    traceId: 'trace-manual',
    trafficSource: 'manual_account_test',
    success: true,
    createdAt: '2026-07-18T10:13:05.078Z'
  }
])
assert.deepEqual(candidates.map((item) => item.id), ['usage-gateway'], '名称查询只能处理会进入首屏热列表的网关记录')

const routeSource = readFileSync(resolve('src/modules/usage-records/usage-records.routes.ts'), 'utf8')
assert.match(routeSource, /getUsageRecordFirstPage/, '使用记录路由必须优先读取首屏热列表')
assert.match(routeSource, /seedUsageRecordFirstPage/, '首屏热列表 miss 后必须回填')
assert.doesNotMatch(routeSource, /getUsageRecordListResponseWithCache/, '使用记录路由不得保留通用读取后响应缓存')

const cacheSource = readFileSync(resolve('src/modules/usage-records/usage-record-first-page-cache.service.ts'), 'utf8')
const repositorySource = readFileSync(resolve('src/storage/usage-records.repository.ts'), 'utf8')
assert.match(cacheSource, /trafficSource === 'gateway'/, '首屏热列表只接收网关请求')
assert.match(cacheSource, /firstPageResponseSize = 20/, '首屏热列表必须固定为 20 条')
assert.match(cacheSource, /publishUsageRecordFirstPage/, '落库后必须更新首屏热列表')
assert.match(cacheSource, /name: 'usage_record_first_page_v2'/, '名称投影修复后必须切换缓存命名空间，使旧坏缓存立即失效')
assert.match(repositorySource, /publishUsageRecordFirstPageWithNamesBestEffort/, '名称 lookup 和热缓存发布必须收口为 best-effort')
assert.match(repositorySource, /usage_record_first_page_cache_publish_failed/, 'best-effort 失败必须保留诊断日志')
assert.doesNotMatch(repositorySource, /loadApiKeyNameMapAsync|loadGroupNameMapAsync|loadAccountNameMapAsync/, '写入热列表不得逐条访问共享名称缓存')
assert.match(repositorySource, /chunkValues\(ids, 900\)/, '写入热列表名称查询必须按数据库参数上限分块')

console.log('使用记录首屏热列表回归通过：默认网关请求首批、写入驱动、miss 回填和通用响应缓存移除均已生效')
