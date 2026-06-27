import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))

const apiKeyModalSource = readSource('frontend/src/views/api-keys/ApiKeyEditModal.vue')
const apiKeyFormSource = readSource('frontend/src/views/api-keys/apiKeyFormModel.ts')
const apiKeysApiSource = readSource('frontend/src/api/domains/apiKeys.ts')
const routeStrategiesViewSource = readSource('frontend/src/views/route-strategies/RouteStrategiesView.vue')
const routeStrategiesApiSource = readSource('frontend/src/api/domains/routeStrategies.ts')
const useScopedDomainApiSource = readSource('frontend/src/composables/useScopedDomainApi.ts')
const routerSource = readSource('frontend/src/router/index.ts')
const accessTypesSource = readSource('frontend/src/types/domain/access.ts')
const accountEndpointModesSource = readSource('frontend/src/views/accounts/accountEndpointModes.ts')

assert(apiKeyModalSource.includes('routeStrategyOptions'), 'API Key 表单必须通过策略路由选项选择调度入口')
assert(apiKeyModalSource.includes('routeStrategyId'), 'API Key 表单必须保存 routeStrategyId')
assert(!apiKeyModalSource.includes('groupBindings'), 'API Key 表单不得继续维护分组绑定')
assert(!apiKeyFormSource.includes('groupBindings'), 'API Key 表单模型不得继续声明分组绑定')
assert(apiKeysApiSource.includes('routeStrategyId?: string'), 'API Key API payload 必须只引用策略路由 ID')
assert(!apiKeysApiSource.includes('groupBindings'), 'API Key API payload 不得继续提交分组绑定')

assert(routeStrategiesViewSource.includes('分组绑定'), '策略路由页面必须承载分组绑定配置')
assert(routeStrategiesViewSource.includes('form.groupBindings'), '策略路由表单必须维护分组绑定行')
assert(routeStrategiesViewSource.includes('groupBindings = form.groupBindings.map'), '策略路由保存必须归一化分组绑定 payload')
assert(routeStrategiesViewSource.includes('请选择分组'), '策略路由表单必须校验分组不能为空')
assert(routeStrategiesViewSource.includes('a-input-number v-model:value="binding.priority"'), '策略路由分组绑定必须维护优先级')
assert(routeStrategiesViewSource.includes('a-input-number v-model:value="binding.weight"'), '策略路由分组绑定必须维护权重')
assert(routeStrategiesViewSource.includes('status: \'active\' | \'disabled\''), '策略路由分组绑定必须支持启停状态')

assert(routeStrategiesApiSource.includes('groupBindings?: Array<'), '策略路由 API payload 必须承载分组绑定数组')
assert(routeStrategiesApiSource.includes('priority?: number'), '策略路由 API payload 必须承载分组优先级')
assert(routeStrategiesApiSource.includes('weight?: number'), '策略路由 API payload 必须承载分组权重')
assert(routeStrategiesApiSource.includes('status?: RouteStrategyGroupBindingStatus'), '策略路由 API payload 必须承载分组绑定状态')
assert(useScopedDomainApiSource.includes('useScopedRouteStrategiesApi'), '前端必须提供作用域化策略路由 API')
assert(routerSource.includes('route-strategies'), '前端路由必须注册策略路由页面')

assert(accessTypesSource.includes('RouteStrategyGroupBindingSummary'), '前端领域类型必须声明策略路由分组绑定摘要')
assert(accessTypesSource.includes('RouteStrategyOptionSummary'), '前端领域类型必须声明策略路由选项摘要')
assert(!accessTypesSource.includes('ApiKeyGroupRouteStrategy'), '前端领域类型不得继续保留 API Key 分组路由策略')

assert(
  accountEndpointModesSource.includes('Chat Completions (JSON)')
    && accountEndpointModesSource.includes('Messages API (Streaming)')
    && accountEndpointModesSource.includes('Count tokens'),
  '接口能力展示应覆盖 OpenAI、Anthropic 和 token 计数能力'
)

console.log('策略路由前端分组绑定回归通过：API Key 不再维护分组绑定，分组、优先级、权重和状态已迁移到策略路由页面与接口')

function readSource(path: string): string {
  return readFileSync(resolve(repoRoot, path), 'utf8')
}
