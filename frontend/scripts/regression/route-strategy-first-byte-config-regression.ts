import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))
const viewSource = readSource('frontend/src/views/route-strategies/RouteStrategiesView.vue')
const typesSource = readSource('frontend/src/types/domain/access.ts')
const apiSource = readSource('frontend/src/api/domains/routeStrategies.ts')

assert(viewSource.includes('form.normal.firstByteDeadlineSeconds'), '普通路由表单必须展示成本/速度共用首字截止秒数')
assert(viewSource.includes('const firstByteDeadlineMs = secondsToMilliseconds(form.normal.firstByteDeadlineSeconds)'), '普通路由保存必须把共用首字截止转换为毫秒')
assert(viewSource.includes('firstByteDeadlineMs,'), '普通路由保存必须写入公共首字截止字段')
assert(viewSource.includes('firstByteDeadlineSeconds: millisecondsToSeconds(config.firstByteDeadlineMs'), '编辑回填必须读取公共首字截止字段')
assert(viewSource.includes('firstByteDeadlineSeconds: 10'), '前端默认首字截止必须为 10 秒')
assert(!viewSource.includes('firstByteThresholdMs'), '前端页面不得把旧速度模式首字阈值作为事实字段')
assert(typesSource.includes('firstByteDeadlineMs: number'), '前端领域类型必须声明公共首字截止字段')
assert(!typesSource.includes('firstByteThresholdMs'), '前端领域类型不得声明旧速度模式首字阈值字段')
assert(apiSource.includes('normalRoutingConfig?: RouteStrategyNormalRoutingConfig | null'), '前端 API payload 必须承载规范化普通路由配置')

console.log('前端策略路由公共首字截止契约回归通过：表单、回填、保存与领域类型均使用统一字段')

function readSource(relativePath: string): string {
  return readFileSync(resolve(repoRoot, relativePath), 'utf8')
}
