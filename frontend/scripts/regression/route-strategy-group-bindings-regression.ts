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
assert(routeStrategiesViewSource.includes('SystemPrincipalSelect'), '策略路由管理页必须复用系统账户选择器限定目标用户')
assert(routeStrategiesViewSource.includes('请先在右侧选择目标系统账户，再创建策略路由'), '管理员创建策略路由前必须先选择具体系统账户')
assert(routeStrategiesViewSource.includes('routeStrategyOperationScopeParams(record)'), '策略路由编辑和删除必须使用记录归属作用域')
assert(routeStrategiesViewSource.includes('systemAccountId: operationScopeParams?.systemAccountId'), '策略路由分组选项必须按当前操作系统账户加载')
assert(routeStrategiesViewSource.includes('form.groupBindings'), '策略路由表单必须维护分组绑定行')
assert(routeStrategiesViewSource.includes('groupBindings = form.groupBindings.map'), '策略路由保存必须归一化分组绑定 payload')
assert(routeStrategiesViewSource.includes('请选择分组'), '策略路由表单必须校验分组不能为空')
assert(routeStrategiesViewSource.includes('v-model:value="binding.priority"'), '策略路由分组绑定必须维护优先级')
assert(routeStrategiesViewSource.includes("bindingShowsPriority = computed(() => form.mode === 'round_robin')"), '混合智能和故障回退路由不得展示分组优先级输入')
assert(routeStrategiesViewSource.includes("bindingShowsDragHandle = computed(() => form.mode === 'hybrid_smart' || form.mode === 'failover')"), '混合智能和故障回退路由必须通过拖拽手柄调整分组顺序')
assert(routeStrategiesViewSource.includes("bindingOrderUsesPosition = computed(() => form.mode === 'hybrid_smart' || form.mode === 'failover')"), '混合智能和故障回退路由必须按当前行顺序生成隐藏优先级')
assert(routeStrategiesViewSource.includes('priority: bindingOrderUsesPosition.value ? index + 1 : binding.priority'), '隐藏优先级路由保存必须按分组添加顺序生成 priority')
assert(routeStrategiesViewSource.includes('bindingRowDragEnabled(index)'), '故障回退路由必须按行控制拖拽范围')
assert(routeStrategiesViewSource.includes('return index > 0 && form.groupBindings.length > 2'), '故障回退路由只能拖拽备用分组排序，不能把备用拖成主用')
assert(routeStrategiesViewSource.includes('bindingRoleText(index)'), '故障回退路由必须展示主用和备用角色')
assert(routeStrategiesViewSource.includes('添加备用分组'), '故障回退路由添加入口必须表达备用分组语义')
assert(routeStrategiesViewSource.includes('故障回退路由的主用分组必须启用'), '故障回退路由必须校验主用分组启用')
assert(routeStrategiesViewSource.includes('故障回退路由至少需要一个启用备用分组'), '故障回退路由必须校验至少一个启用备用分组')
assert(routeStrategiesViewSource.includes('HolderOutlined'), '隐藏优先级路由分组排序必须展示拖拽图标')
assert(routeStrategiesViewSource.includes('v-model:value="binding.weight"'), '策略路由分组绑定必须维护权重')
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

console.log('策略路由前端分组绑定回归通过：API Key 只选择路由策略，分组、优先级、权重和状态由策略路由页面与接口维护')

function readSource(path: string): string {
  return readFileSync(resolve(repoRoot, path), 'utf8')
}
