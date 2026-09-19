import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))
const viewSource = readSource('frontend/src/views/route-strategies/RouteStrategiesView.vue')
const typesSource = readSource('frontend/src/types/domain/access.ts')
const apiSource = readSource('frontend/src/api/domains/routeStrategies.ts')

assert(
  viewSource.includes("const schedulingPreferenceSupportedModes: ReadonlyArray<RouteStrategyMode> = ['normal', 'weighted', 'failover', 'round_robin', 'merge']"),
  '调度偏好必须对 normal/weighted/failover/round_robin/merge 五种模式开放'
)
assert(viewSource.includes('v-if="schedulingPreferenceSupported"'), '调度偏好区块必须由多模式计算属性控制展示')
assert(!viewSource.includes("record.mode !== 'normal'"), '模式展示文本不得再把调度偏好限定在 normal 模式')
assert(
  viewSource.includes('schedulingPreferenceSupportedModes.includes(record.mode) && record.normalRoutingConfig?.schedulingPreference === \'speed_first\''),
  '速度优先判断必须按多模式集合加调度偏好取值判断'
)
assert(viewSource.includes('v-if="form.normal.schedulingPreference === \'speed_first\'" label="首字截止"'), '首字截止控件只能在速度优先时展示')
assert(viewSource.includes('const firstByteDeadlineMs = secondsToMilliseconds(form.normal.firstByteDeadlineSeconds)'), '速度优先保存必须把首字截止转换为毫秒')
assert(viewSource.includes("return { schedulingPreference: 'cost_first' }"), '成本优先 payload 不得携带首字截止')
assert(viewSource.includes("config.schedulingPreference === 'speed_first'"), '编辑回填必须仅在速度优先时读取首字截止')
assert(viewSource.includes('firstByteDeadlineSeconds: 30'), '前端默认首字截止必须为 30 秒')
assert(!viewSource.includes('firstByteThresholdMs'), '前端页面不得把旧速度模式首字阈值作为事实字段')
assert(typesSource.includes('firstByteDeadlineMs: number'), '前端领域类型必须声明公共首字截止字段')
assert(!typesSource.includes('firstByteThresholdMs'), '前端领域类型不得声明旧速度模式首字阈值字段')
assert(apiSource.includes('normalRoutingConfig?: RouteStrategyNormalRoutingConfig | null'), '前端 API payload 必须承载规范化普通路由配置')

const payloadSource = sliceBetween(viewSource, 'function buildRouteStrategyFormPayload', 'async function deleteRouteStrategy')
const schedulingPreferenceElseBranch = sliceBetween(payloadSource, '} else {', 'return payload')
assert(
  schedulingPreferenceElseBranch.includes('payload.normalRoutingConfig = buildNormalRoutingConfigPayload()'),
  'weighted/failover/round_robin/merge 必须与 normal 同源提交真实调度偏好配置'
)
assert(schedulingPreferenceElseBranch.includes('weighted/failover/round_robin/merge'), '调度偏好提交分支注释必须把 merge 纳入共享模式清单')

console.log('前端策略路由速度优先首字截止契约回归通过：调度偏好对五种模式开放，仅速度优先展示和保存首字截止，默认 30 秒')

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
