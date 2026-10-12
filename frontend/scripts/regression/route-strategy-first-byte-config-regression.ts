import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  buildSchedulingPreferenceFormPayload,
  defaultSpeedFirstParamsForm,
  schedulingPreferenceSpeedFirstBounds
} from '../../src/views/scheduling-preferences/schedulingPreferenceForm'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))
const viewSource = readSource('frontend/src/views/route-strategies/RouteStrategiesView.vue')
const schedulingPreferenceViewSource = readSource('frontend/src/views/scheduling-preferences/SchedulingPreferencesView.vue')
const typesSource = readSource('frontend/src/types/domain/access.ts')
const apiSource = readSource('frontend/src/api/domains/routeStrategies.ts')

// —— 策略路由侧（P2 终态）：调度偏好独立实体后编辑器回归纯拓扑职责，不再配置、
// 不再回填、不再提交任何调度字段；速度运行态入口按历史策略行配置事实独立保留。——
assert(
  viewSource.includes("const speedFirstRuntimeSupportedModes: ReadonlyArray<RouteStrategyMode> = ['normal', 'weighted', 'failover', 'round_robin', 'merge']"),
  '速度运行态判断必须覆盖 normal/weighted/failover/round_robin/merge 五种模式'
)
assert(!viewSource.includes("record.mode !== 'normal'"), '模式展示文本不得再把运行态判断限定在 normal 模式')
assert(
  viewSource.includes("speedFirstRuntimeSupportedModes.includes(record.mode) && record.normalRoutingConfig?.schedulingPreference === 'speed_first'"),
  '速度优先运行态入口必须按多模式集合加历史策略行配置事实判断'
)
assert(
  !viewSource.includes('label="调度偏好"') && !viewSource.includes('<span>调度偏好</span>'),
  '策略路由编辑器不得再渲染调度偏好配置区块（已迁往调度偏好页）'
)
assert(!viewSource.includes('form.normal'), '策略路由编辑器不得再绑定调度偏好表单字段')
assert(!viewSource.includes('firstByteThresholdMs'), '前端页面不得把旧速度模式首字阈值作为事实字段')

// —— 调度偏好页（速度优先参数新家）：首字截止等九字段契约原位等价。——
assert(schedulingPreferenceViewSource.includes("v-if=\"form.chatMode === 'speed_first'\"") && schedulingPreferenceViewSource.includes('label="首字截止"'), '首字截止控件只能在速度优先时展示（调度偏好页）')
assert(
  schedulingPreferenceViewSource.includes('schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.min')
    && schedulingPreferenceViewSource.includes('schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.max'),
  '首字截止输入必须绑定调度偏好表单并按契约边界限制'
)
assert.equal(schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.fallback, 30000, '前端默认首字截止必须为 30000 毫秒（等价旧默认 30 秒）')
const costFirstPayload = buildSchedulingPreferenceFormPayload({ name: '成本优先', chatMode: 'cost_first', enabled: true, speedFirst: defaultSpeedFirstParamsForm() })
assert.equal('chatSpeedFirstConfig' in costFirstPayload, false, '成本优先提交体不得携带 chatSpeedFirstConfig（等价旧「成本优先 payload 不携带首字截止」契约）')

// —— 类型与 API 写侧（P2 旧调度字段退场）。——
assert(typesSource.includes('firstByteDeadlineMs: number'), '前端领域类型必须声明公共首字截止字段（调度偏好配置与历史策略行运行态事实共用）')
assert(!typesSource.includes('firstByteThresholdMs'), '前端领域类型不得声明旧速度模式首字阈值字段')
const payloadTypeSource = sliceBetween(apiSource, 'export interface RouteStrategyMutationPayload', 'export interface RouteStrategyPatchPayload')
for (const legacyField of ['normalRoutingConfig', 'schedulingPreference', 'firstByteDeadlineMs', 'speedFirstConfig'] as const) {
  assert(!payloadTypeSource.includes(`${legacyField}?`), `策略路由前端 API payload 不得再声明调度字段 ${legacyField}（P2 写侧退场，后端将拒绝）`)
}

const payloadSource = sliceBetween(viewSource, 'function buildRouteStrategyFormPayload', 'async function deleteRouteStrategy')
assert(
  payloadSource.includes('name: form.name.trim()')
    && payloadSource.includes('groupBindings: form.groupBindings.map'),
  '策略路由保存必须仍组装名称与分组绑定等拓扑字段'
)
assert(
  !payloadSource.includes('schedulingPreference') && !payloadSource.includes('firstByteDeadlineMs') && !payloadSource.includes('speedFirstConfig'),
  '策略路由保存 payload 不得组装任何调度字段（weighted/failover/round_robin/merge 与 normal 一致回归纯拓扑职责）'
)

console.log('前端策略路由速度优先首字截止契约回归通过：策略编辑器不再配置或提交调度字段，首字截止契约等价迁往调度偏好页（默认 30000 毫秒、范围 10000-60000 毫秒、仅速度优先携带）')

function readSource(relativePath: string): string {
  return readFileSync(resolve(repoRoot, relativePath), 'utf8')
}

function sliceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  assert.notEqual(startIndex, -1, `缺少源码片段起点：${start}`)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(endIndex, -1, `缺少源码片段终点：${end}`)
  return source.slice(startIndex, endIndex)
}
