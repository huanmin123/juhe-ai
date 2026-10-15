import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import type { ApiKeyMutationResult, ApiKeySummary } from '../../src/types/domain'
import {
  buildApiKeyCreatePayload,
  buildApiKeyMutationPatch,
  hasApiKeyMutationChanges,
  mergeApiKeyMutationResult,
  type ApiKeyEditableSnapshot
} from '../../src/views/api-keys/apiKeyMutation'

const frontendRoot = resolve(import.meta.dirname, '../..')
const apiSource = readFileSync(resolve(frontendRoot, 'src/api/domains/apiKeys.ts'), 'utf8')
const modalSource = readFileSync(resolve(frontendRoot, 'src/views/api-keys/ApiKeyEditModal.vue'), 'utf8')
const rowActionsSource = readFileSync(resolve(frontendRoot, 'src/views/api-keys/useApiKeyRowActions.ts'), 'utf8')

const baseline: ApiKeyEditableSnapshot = {
  name: 'Key A',
  routeStrategyId: 'route_1',
  schedulingPreferenceId: 'pref_1',
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

const schedulingPreferencePatch = buildApiKeyMutationPatch(baseline, {
  ...structuredClone(baseline),
  schedulingPreferenceId: 'pref_2'
})
assert.deepEqual(schedulingPreferencePatch, { schedulingPreferenceId: 'pref_2' }, '只改调度偏好时只能发送 schedulingPreferenceId')

// 偏好清空不是「缺失保持原值」：契约要求显式回默认（内置 ID）。
const schedulingPreferenceResetPatch = buildApiKeyMutationPatch(baseline, {
  ...structuredClone(baseline),
  schedulingPreferenceId: undefined
})
assert.deepEqual(schedulingPreferenceResetPatch, { schedulingPreferenceId: 'schpref-builtin-chat-cost-first' }, '清空调度偏好必须显式提交内置默认 ID')

// 历史 NULL 偏好（迁移期）未交互时不得产生偏好 delta。
const legacyBaseline: ApiKeyEditableSnapshot = { ...structuredClone(baseline), schedulingPreferenceId: undefined }
assert.deepEqual(
  buildApiKeyMutationPatch(legacyBaseline, structuredClone(legacyBaseline)),
  {},
  '历史 NULL 偏好未变更时 PATCH 不得携带 schedulingPreferenceId'
)

const defaultCreatePayload = buildApiKeyCreatePayload({
  name: 'Key B',
  routeStrategyId: 'route_default',
  status: 'active',
  expiresAt: null,
  description: '',
  quotaLimits: {},
  availabilitySchedule: null
}, { routeStrategyTouched: false })
// 契约变更（PLAN-20261012T033000000Z P1）：创建总是提交显式偏好 ID，未选择时持久化内置默认行。
assert.deepEqual(defaultCreatePayload, {
  name: 'Key B',
  schedulingPreferenceId: 'schpref-builtin-chat-cost-first'
}, '新建必须携带显式调度偏好 ID：未选择时提交内置默认调度偏好')
assert.deepEqual(buildApiKeyCreatePayload({
  ...baseline,
  description: '  configured  '
}, { routeStrategyTouched: true }), {
  name: baseline.name,
  routeStrategyId: baseline.routeStrategyId,
  schedulingPreferenceId: baseline.schedulingPreferenceId,
  expiresAt: baseline.expiresAt,
  description: 'configured',
  quotaLimits: baseline.quotaLimits,
  availabilitySchedule: baseline.availabilitySchedule
}, '新建提交用户选择的偏好 ID 与实际配置的非默认字段')

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
  schedulingPreferenceId: baseline.schedulingPreferenceId,
  schedulingPreferenceName: '偏好一',
  schedulingPreferenceBuiltIn: false,
  quotaLimits: baseline.quotaLimits,
  availabilitySchedule: baseline.availabilitySchedule ?? undefined,
  usage: { requestCount: 1, totalTokens: 2, totalCost: 0.1 }
}
const result: ApiKeyMutationResult = {
  id: current.id,
  revision: 'revision-2',
  changedFields: ['description', 'availabilitySchedule'],
  rowPatch: {
    revision: 'revision-2',
    description: null,
    availabilitySchedule: null,
    schedulingPreferenceId: 'schpref-builtin-chat-cost-first',
    schedulingPreferenceName: '默认调度偏好',
    schedulingPreferenceBuiltIn: true
  }
}
const merged = mergeApiKeyMutationResult(current, result)
assert.equal(merged.revision, 'revision-2', '最小 mutation result 必须推进列表 revision')
assert.equal(merged.description, undefined, 'rowPatch null 必须在列表行归一化为缺失值')
assert.equal(merged.availabilitySchedule, undefined, '清空时间计划后列表行不得保留旧值')
assert.equal(merged.schedulingPreferenceId, 'schpref-builtin-chat-cost-first', 'rowPatch 必须回写调度偏好绑定 ID')
assert.equal(merged.schedulingPreferenceName, '默认调度偏好', 'rowPatch 必须回写调度偏好名称供回显')
assert.equal(merged.schedulingPreferenceBuiltIn, true, 'rowPatch 必须回写内置标识')
assert.deepEqual(merged.usage, current.usage, '最小 mutation result 不得覆盖未返回的 usage')

const nulledPreference = mergeApiKeyMutationResult(current, {
  id: current.id,
  revision: 'revision-3',
  changedFields: [],
  rowPatch: { revision: 'revision-3', schedulingPreferenceId: null, schedulingPreferenceName: null, schedulingPreferenceBuiltIn: null }
})
assert.equal(nulledPreference.schedulingPreferenceId, undefined, 'rowPatch null 偏好绑定必须在列表行归一化为缺失值')
assert.equal(nulledPreference.schedulingPreferenceName, undefined, 'rowPatch null 偏好名称必须在列表行归一化为缺失值')
assert.equal(nulledPreference.schedulingPreferenceBuiltIn, undefined, 'rowPatch null 内置标识必须在列表行归一化为缺失值')

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
assert.match(createBranch, /buildApiKeyCreatePayload\(snapshot,[\s\S]*routeStrategyTouched:\s*routeStrategyTouched\.value/, '新建必须通过最小命令构造器区分缓存默认路由与用户交互')
assert.doesNotMatch(createBranch, /\.\.\.\(snapshot\.routeStrategyId\s*\?/, '新建不得因缓存默认值自动提交路由')
const deleteBranch = sourceBetween(rowActionsSource, 'async function removeApiKey', '\n\n  return {')
assert.match(deleteBranch, /await input\.apiKeysApi\.delete[\s\S]*input\.removeItems/, '删除成功必须按本地结果移除列表行')
assert.doesNotMatch(deleteBranch, /input\.reload\s*\(/, '删除成功后不得重复刷新 API Key 列表')

assert(
  apiSource.includes("http.patch(`/api-keys/${encodeURIComponent(id)}`, payload, { params })"),
  '管理员 API Key 更新必须编码动态路径段'
)
assert(
  apiSource.includes("http.patch(`/my-api-keys/${encodeURIComponent(id)}`, payload)"),
  '个人 API Key 更新必须编码动态路径段'
)

// 调度偏好绑定（PLAN-20261012T033000000Z P1）：弹窗选择器与快照接入。
assert.match(modalSource, /schedulingPreferencesApi:\s*Pick<ScopedSchedulingPreferencesApi,\s*'options'>/, '弹窗必须声明调度偏好 options 依赖')
assert.match(modalSource, /v-if="schedulingPreferenceSelectorVisible"[^>]*label="调度偏好"/, '调度偏好选择器必须按可见性条件渲染')
assert.match(modalSource, /schedulingPreferencePurposeExempt\(editingPurpose\.value\)\) return undefined/, 'purpose=chat 的快照必须强制缺失偏好 ID，任何路径不提交')
assert.doesNotMatch(modalSource, /'studio'/, '调度偏好豁免只接受 chat，API Key 词表不得残留已下线的 studio 用途')
assert.match(sourceBetween(modalSource, 'function openCreate', 'function openEdit'), /schedulingPreferenceId:\s*SCHEDULING_PREFERENCE_BUILTIN_CHAT_COST_FIRST_ID/, '新建必须预选内置默认调度偏好')
assert.match(sourceBetween(modalSource, 'function openEdit', 'function apiKeyOperationScopeParams'), /schedulingPreferenceId:\s*apiKey\.schedulingPreferenceId \?\? ''/, '编辑必须从列表行回填调度偏好绑定（历史 NULL 显示占位）')

// 调度偏好 v2 用户裁决：偏好必选无空态；内置行显示名「默认调度偏好」由后端 options/name 下发。
const schedulingPreferenceField = sourceBetween(modalSource, 'label="调度偏好"', '</a-form-item>')
assert.match(schedulingPreferenceField, /required/, '调度偏好选择器必须标记必选')
assert.doesNotMatch(schedulingPreferenceField, /allow-clear/, '调度偏好选择器必选，不得提供清空入口（无空态）')
assert.match(modalSource, /请选择调度偏好/, '编辑保存必须校验调度偏好必选（无「未选择」可提交态）')
assert.doesNotMatch(modalSource, /系统默认（/, '内置行显示名由后端下发，前端不得拼装「系统默认（…）」包装')

console.log('API Key delta PATCH 与最小 mutation result 回归测试通过')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
