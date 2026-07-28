import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import type { ApiKeyMutationResult, ApiKeySummary } from '../../src/types/domain'
import {
  buildApiKeyMutationPatch,
  hasApiKeyMutationChanges,
  mergeApiKeyMutationResult,
  type ApiKeyEditableSnapshot
} from '../../src/views/api-keys/apiKeyMutation'

const frontendRoot = resolve(import.meta.dirname, '../..')
const apiSource = readFileSync(resolve(frontendRoot, 'src/api/domains/apiKeys.ts'), 'utf8')
const modalSource = readFileSync(resolve(frontendRoot, 'src/views/api-keys/ApiKeyEditModal.vue'), 'utf8')

const baseline: ApiKeyEditableSnapshot = {
  name: 'Key A',
  routeStrategyId: 'route_1',
  status: 'active',
  expiresAt: '2026-08-01T00:00:00.000Z',
  description: 'before',
  quotaLimits: { daily: { enabled: true, limit: 5 } },
  availabilitySchedule: {
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [{ daysOfWeek: [1], start: '09:00', end: '18:00' }]
  }
}

const unchanged = buildApiKeyMutationPatch(baseline, structuredClone(baseline))
assert.deepEqual(unchanged, {}, '未修改表单必须生成空 PATCH')
assert.equal(hasApiKeyMutationChanges(unchanged), false, '空 PATCH 必须被识别为 no-op')

const descriptionPatch = buildApiKeyMutationPatch(baseline, { ...structuredClone(baseline), description: 'after' })
assert.deepEqual(descriptionPatch, { description: 'after' }, '只改说明时只能发送 description')

const quotaPatch = buildApiKeyMutationPatch(baseline, {
  ...structuredClone(baseline),
  quotaLimits: { daily: { enabled: true, limit: 6 } }
})
assert.deepEqual(quotaPatch, { quotaLimits: { daily: { enabled: true, limit: 6 } } }, '额度变化只能整体替换 quotaLimits 聚合')

const clearPatch = buildApiKeyMutationPatch(baseline, {
  ...structuredClone(baseline),
  expiresAt: null,
  availabilitySchedule: null
})
assert.deepEqual(clearPatch, { expiresAt: null, availabilitySchedule: null }, '清空可空字段必须显式发送 null')

const current: ApiKeySummary = {
  id: 'key_1',
  revision: 'revision-1',
  name: baseline.name,
  description: baseline.description,
  keyPrefix: 'sk-old',
  keySuffix: 'suffix',
  status: 'active',
  purpose: 'general',
  routeStrategyId: 'route_1',
  quotaLimits: baseline.quotaLimits,
  availabilitySchedule: baseline.availabilitySchedule ?? undefined,
  usage: { requestCount: 1, totalTokens: 2, totalCost: 0.1 }
}
const result: ApiKeyMutationResult = {
  id: current.id,
  revision: 'revision-2',
  changedFields: ['description', 'availabilitySchedule'],
  rowPatch: { revision: 'revision-2', description: null, availabilitySchedule: null }
}
const merged = mergeApiKeyMutationResult(current, result)
assert.equal(merged.revision, 'revision-2', '最小 mutation result 必须推进列表 revision')
assert.equal(merged.description, undefined, 'rowPatch null 必须在列表行归一化为缺失值')
assert.equal(merged.availabilitySchedule, undefined, '清空时间计划后列表行不得保留旧值')
assert.deepEqual(merged.usage, current.usage, '最小 mutation result 不得覆盖未返回的 usage')

assert.match(apiSource, /interface ApiKeyUpdatePayload[\s\S]*expectedRevision:\s*string/, 'PATCH payload 必须携带 expectedRevision')
assert.match(apiSource, /unwrap<ApiKeyMutationResult>\(http\.patch/, 'PATCH 响应必须使用最小 mutation result')
assert.match(apiSource, /quotaLimits\?:\s*ApiKeyQuotaLimits\s*\|\s*null/, 'PATCH 必须允许 quotaLimits: null 清空额度')

const updateBranch = sourceBetween(modalSource, 'if (targetId) {', '} else {')
const createBranch = sourceBetween(modalSource, 'const result = await props.apiKeysApi.create', "emit('created'")
assert.match(updateBranch, /buildApiKeyMutationPatch\(editingBaseline, snapshot\)/, '编辑保存必须相对打开 baseline 构造 delta')
assert.match(updateBranch, /hasApiKeyMutationChanges\(patch\)/, '编辑保存必须拦截 no-op')
assert.match(updateBranch, /expectedRevision:\s*editingRevision/, '编辑 PATCH 必须提交列表 revision')
assert.doesNotMatch(updateBranch, /emit\('reload'/, '编辑成功后不得追加列表刷新')
assert.match(modalSource, /@update:value="markRouteStrategyTouched"/, '只有用户实际选择路由时才标记交互')
assert.match(createBranch, /routeStrategyTouched\.value && snapshot\.routeStrategyId/, '新建时未交互的缓存默认路由只用于展示，不得提交 routeStrategyId')
assert.doesNotMatch(createBranch, /\.\.\.\(snapshot\.routeStrategyId\s*\?/, '新建不得因缓存默认值自动提交路由')

assert(
  apiSource.includes("http.patch(`/api-keys/${encodeURIComponent(id)}`, payload, { params })"),
  '管理员 API Key 更新必须编码动态路径段'
)
assert(
  apiSource.includes("http.patch(`/my-api-keys/${encodeURIComponent(id)}`, payload)"),
  '个人 API Key 更新必须编码动态路径段'
)

console.log('API Key delta PATCH 与最小 mutation result 回归测试通过')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
