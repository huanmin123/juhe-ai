import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))

const apiKeyModalSource = readSource('frontend/src/views/api-keys/ApiKeyEditModal.vue')
const apiKeysApiSource = readSource('frontend/src/api/domains/apiKeys.ts')
const routeStrategiesViewSource = readSource('frontend/src/views/route-strategies/RouteStrategiesView.vue')
const routeStrategiesApiSource = readSource('frontend/src/api/domains/routeStrategies.ts')
const accessTypesSource = readSource('frontend/src/types/domain/access.ts')

assert(apiKeyModalSource.includes('routeStrategyId'), 'API Key 表单必须只选择策略路由 ID')
assert(apiKeyModalSource.includes('策略路由'), 'API Key 表单必须展示策略路由选择')
assert(!apiKeyModalSource.includes('hybridRoutingConfigJson'), 'API Key 表单不得继续展示混合智能路由 JSON')
assert(!apiKeyModalSource.includes('scoringFallbackMaxLevel'), 'API Key 表单不得继续维护混合智能路由评分兜底字段')
assert(!apiKeyModalSource.includes('explicitHybridRouteRules'), 'API Key 表单不得继续维护显式跨协议规则')
assert(!apiKeyModalSource.includes('clientProfile'), 'API Key 表单不得继续维护默认客户端画像')

assert(apiKeysApiSource.includes('routeStrategyId?: string'), 'API Key 前端请求层必须声明 routeStrategyId')
assert(!apiKeysApiSource.includes('payload: Record<string, unknown>'), 'API Key 前端请求层不得继续使用宽泛 Record payload')
assert(!apiKeysApiSource.includes('hybridRoutingConfig'), 'API Key 请求层不得继续提交混合智能路由配置')
assert(!apiKeysApiSource.includes('explicitHybridRouteRules'), 'API Key 请求层不得继续提交显式跨协议规则')

assert(routeStrategiesViewSource.includes('scoringFallbackMaxLevel'), '混合智能路由配置必须迁移到策略路由页面')
assert(routeStrategiesViewSource.includes('混合智能配置'), '策略路由页面必须展示结构化混合智能路由配置入口')
assert(routeStrategiesViewSource.includes("form.mode === 'hybrid_smart'"), '策略路由页面必须只在 hybrid_smart 模式下展示混合配置')
assert(routeStrategiesViewSource.includes('defaultHybridRoutingForm'), '策略路由页面必须维护结构化混合智能配置默认值')
assert(routeStrategiesViewSource.includes('buildHybridRoutingConfigPayload'), '策略路由页面必须从结构化表单生成混合智能配置')
assert(routeStrategiesViewSource.includes('payload.hybridRoutingConfig = hybridRoutingConfig'), '策略路由保存必须把混合配置提交到策略路由接口')
assert(routeStrategiesViewSource.includes('payload.hybridRoutingConfig = null'), '非混合智能模式必须清空混合配置')

assert(routeStrategiesApiSource.includes('export interface RouteStrategyMutationPayload'), '策略路由请求层必须使用显式 mutation payload 类型')
assert(routeStrategiesApiSource.includes('hybridRoutingConfig?: ApiKeyHybridRoutingConfig | null'), '策略路由请求层必须承载混合智能路由配置')
assert(routeStrategiesApiSource.includes('groupBindings?: Array<'), '策略路由请求层必须承载分组绑定')

for (const mode of ['normal', 'round_robin', 'weighted', 'failover', 'hybrid_smart']) {
  assert(accessTypesSource.includes(`'${mode}'`), `前端领域类型必须声明策略路由模式：${mode}`)
}
assert(accessTypesSource.includes('scoringFallbackMaxLevel: number'), '前端领域类型必须保留混合智能评分不可用兜底上限字段')

console.log('策略路由混合智能前端配置回归通过：API Key 只绑定策略路由，混合智能配置已迁移到策略路由页面和接口')

function readSource(path: string): string {
  return readFileSync(resolve(repoRoot, path), 'utf8')
}
