import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  defaultNormalRoutingConfig,
  normalizeNormalRoutingConfig,
  parseRouteStrategyRuntimeConfigJson,
  routeStrategyConfigJson
} from '../../domain/route-strategy.js'

const defaultConfig = defaultNormalRoutingConfig()
assert.deepEqual(defaultConfig, {
  schedulingPreference: 'cost_first'
}, '普通路由默认成本优先不得创建首字截止')

const costFirst = normalizeNormalRoutingConfig({
  schedulingPreference: 'cost_first',
  firstByteDeadlineMs: 20_000
})
assert.equal(Object.hasOwn(costFirst, 'firstByteDeadlineMs'), false, '成本优先必须丢弃旧公共首字截止')
assert.equal(Object.hasOwn(costFirst, 'speedFirstConfig'), false, '成本优先不得生成速度优先专属配置')

const speedFirst = normalizeNormalRoutingConfig({
  schedulingPreference: 'speed_first',
  firstByteDeadlineMs: 30_000,
  speedFirstConfig: {
    slowTriggerCount: 4,
    maxFirstByteRetriesPerRequest: 3
  }
})
assert.equal(speedFirst.firstByteDeadlineMs, 30_000, '速度优先必须读取同一个公共首字截止')
assert.equal(speedFirst.speedFirstConfig?.slowTriggerCount, 4, '速度优先必须保留慢样本参数')
assert.equal(speedFirst.speedFirstConfig?.maxFirstByteRetriesPerRequest, 3, '速度优先必须保留单请求切号上限')
assert.equal(Object.hasOwn(speedFirst.speedFirstConfig ?? {}, 'firstByteThresholdMs'), false, '规范化速度配置不得输出旧字段')

const speedFirstDefault = normalizeNormalRoutingConfig({ schedulingPreference: 'speed_first' })
assert.equal(speedFirstDefault.firstByteDeadlineMs, 30_000, '速度优先首字截止默认必须为 30 秒')

const legacy = normalizeNormalRoutingConfig({
  schedulingPreference: 'speed_first',
  speedFirstConfig: {
    firstByteThresholdMs: 25_000,
    slowTriggerCount: 5
  }
})
assert.equal(legacy.firstByteDeadlineMs, 25_000, '旧首字阈值必须映射为公共首字截止')
assert.equal(legacy.speedFirstConfig?.slowTriggerCount, 5, '旧配置迁移时必须保留速度优先参数')

assert.throws(() => normalizeNormalRoutingConfig({
  schedulingPreference: 'speed_first',
  firstByteDeadlineMs: 10_000,
  speedFirstConfig: { firstByteThresholdMs: 20_000 }
}), /不能同时配置/, '新旧首字字段同时出现必须拒绝')
assert.throws(() => normalizeNormalRoutingConfig({ schedulingPreference: 'speed_first', firstByteDeadlineMs: 9_999 }), /10000-60000/, '速度优先首字截止不能低于 10 秒')
assert.throws(() => normalizeNormalRoutingConfig({ schedulingPreference: 'speed_first', firstByteDeadlineMs: 60_001 }), /10000-60000/, '速度优先首字截止不能高于 60 秒')

const serialized = routeStrategyConfigJson({ normalRoutingConfig: legacy })
assert(serialized, '速度优先配置必须生成持久化 JSON')
assert.equal(serialized.includes('firstByteDeadlineMs'), true, '持久化配置必须写公共首字截止')
assert.equal(serialized.includes('firstByteThresholdMs'), false, '持久化配置不得写兼容别名')
const reparsed = parseRouteStrategyRuntimeConfigJson(serialized)
assert.deepEqual(reparsed.normalRoutingConfig, legacy, '公共字段序列化往返必须稳定')

const routeApiSource = readFileSync(new URL('../../modules/route-strategies/route-strategies.routes.ts', import.meta.url), 'utf8')
const externalApiSource = readFileSync(new URL('../../modules/external-integrations/external-integrations.routes.ts', import.meta.url), 'utf8')
for (const [source, label] of [[routeApiSource, '管理 API'], [externalApiSource, '外部集成 API']] as const) {
  assert(source.includes('firstByteDeadlineMs'), `${label} schema 必须接受公共首字截止`)
  assert(source.includes('firstByteThresholdMs'), `${label} schema 必须保留旧字段读取兼容入口`)
}

console.log('策略路由速度优先首字截止配置回归通过：默认 30 秒、成本优先排除、范围与旧字段迁移均正确')
