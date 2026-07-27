import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))
const viewSource = readSource('frontend/src/views/route-strategies/RouteStrategiesView.vue')
const typesSource = readSource('frontend/src/types/domain/access.ts')
const apiSource = readSource('frontend/src/api/domains/routeStrategies.ts')

assert(viewSource.includes('v-if="form.normal.schedulingPreference === \'speed_first\'" label="首字截止"'), '首字截止控件只能在速度优先时展示')
assert(viewSource.includes('const firstByteDeadlineMs = secondsToMilliseconds(form.normal.firstByteDeadlineSeconds)'), '速度优先保存必须把首字截止转换为毫秒')
assert(viewSource.includes("return { schedulingPreference: 'cost_first' }"), '成本优先 payload 不得携带首字截止')
assert(viewSource.includes("config.schedulingPreference === 'speed_first'"), '编辑回填必须仅在速度优先时读取首字截止')
assert(viewSource.includes('firstByteDeadlineSeconds: 30'), '前端默认首字截止必须为 30 秒')
assert(!viewSource.includes('firstByteThresholdMs'), '前端页面不得把旧速度模式首字阈值作为事实字段')
assert(typesSource.includes('firstByteDeadlineMs: number'), '前端领域类型必须声明公共首字截止字段')
assert(!typesSource.includes('firstByteThresholdMs'), '前端领域类型不得声明旧速度模式首字阈值字段')
assert(apiSource.includes('normalRoutingConfig?: RouteStrategyNormalRoutingConfig | null'), '前端 API payload 必须承载规范化普通路由配置')

console.log('前端策略路由速度优先首字截止契约回归通过：仅速度优先展示和保存，默认 30 秒')

function readSource(relativePath: string): string {
  return readFileSync(resolve(repoRoot, relativePath), 'utf8')
}
