import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

import { loadGroupOptionsResource } from '../../src/composables/useGroupOptionsResource'
import { loadRouteStrategyOptionsResource } from '../../src/composables/useRouteStrategyOptionsResource'
import type { RouteStrategyGroupOption, RouteStrategyOptionSummary } from '../../src/types/domain'

const repoRoot = resolve(fileURLToPath(new URL('../../..', import.meta.url)))

const apiKeyModalSource = readSource('frontend/src/views/api-keys/ApiKeyEditModal.vue')
const routeStrategyOptionsResourceSource = readSource('frontend/src/composables/useRouteStrategyOptionsResource.ts')
const apiKeyFormSource = readSource('frontend/src/views/api-keys/apiKeyFormModel.ts')
const apiKeysApiSource = readSource('frontend/src/api/domains/apiKeys.ts')
const apiKeysViewSource = readSource('frontend/src/views/api-keys/ApiKeysView.vue')
const routeStrategiesViewSource = readSource('frontend/src/views/route-strategies/RouteStrategiesView.vue')
const groupOptionsResourceSource = readSource('frontend/src/composables/useGroupOptionsResource.ts')
const routeStrategiesApiSource = readSource('frontend/src/api/domains/routeStrategies.ts')
const useScopedDomainApiSource = readSource('frontend/src/composables/useScopedDomainApi.ts')
const routerSource = readSource('frontend/src/router/index.ts')
const accessTypesSource = readSource('frontend/src/types/domain/access.ts')
const accountEndpointModesSource = readSource('frontend/src/views/accounts/accountEndpointModes.ts')

await assertRouteStrategySelectedOptionMerge()
await assertGroupSelectedOptionMerge()
await assertKnownGroupSelectedOptionMerge()

assert(apiKeyModalSource.includes('routeStrategyOptions'), 'API Key 表单必须通过策略路由选项选择调度入口')
assert(apiKeyModalSource.includes('routeStrategyId'), 'API Key 表单必须保存 routeStrategyId')
assert(apiKeyModalSource.includes('routeStrategyOptionsRequestToken'), 'API Key 表单策略路由下拉必须防止编辑回填、打开下拉和远程搜索请求互相覆盖')
assert(apiKeyModalSource.includes('loadRouteStrategyOptionsResource'), 'API Key 表单策略路由下拉应复用统一持久资源缓存')
assert(routeStrategyOptionsResourceSource.includes('missingIds'), 'API Key 表单策略路由下拉应在主窗口请求后单独补齐缺失的已选策略')
assert(routeStrategyOptionsResourceSource.includes('activeOnly: false'), 'API Key 表单策略路由下拉必须能回显停用的已绑定策略，并由前端禁用停用选项')
assert(!/ids,\s*limit:\s*100/.test(apiKeyModalSource), 'API Key 表单策略路由主窗口请求不能用已选 ID 过滤，否则编辑时下拉只剩当前策略')
assert(apiKeysViewSource.includes('routeStrategyOptionsRequestToken'), 'API Key 列表策略路由筛选必须防止旧搜索结果覆盖新选项')
assert(apiKeysViewSource.includes('routeStrategyOptionsLoadingKey === requestKey'), 'API Key 列表策略路由筛选相同请求进行中时必须复用请求')
assert(apiKeysViewSource.includes('loadRouteStrategyOptionsResource'), 'API Key 列表策略路由筛选应复用统一持久资源缓存')
assert(!apiKeyModalSource.includes('groupBindings'), 'API Key 表单不得继续维护分组绑定')
assert(!apiKeyFormSource.includes('groupBindings'), 'API Key 表单模型不得继续声明分组绑定')
assert(apiKeysApiSource.includes('routeStrategyId?: string'), 'API Key API payload 必须只引用策略路由 ID')
assert(!apiKeysApiSource.includes('groupBindings'), 'API Key API payload 不得继续提交分组绑定')

assert(routeStrategiesViewSource.includes('分组绑定'), '策略路由页面必须承载分组绑定配置')
assert(routeStrategiesViewSource.includes('用于在 API Key 中识别这套路由策略'), '策略路由名称字段必须提供用途说明')
assert(routeStrategiesViewSource.includes('bindingSectionTooltip'), '策略路由分组绑定区域必须提供随模式变化的说明')
assert(routeStrategiesViewSource.includes('InfoCircleOutlined'), '策略路由技术字段必须展示说明图标')
assert(routeStrategiesViewSource.includes('先用这个模型判断请求难度和适合的能力档位'), '混合智能评分模型必须提供字段说明')
assert(routeStrategiesViewSource.includes('把评分等级 1-10 映射到目标模型'), '混合智能等级模型必须提供配置说明')
assert(routeStrategiesViewSource.includes('SystemPrincipalSelect'), '策略路由管理页必须复用系统账户选择器限定目标用户')
assert(routeStrategiesViewSource.includes('请先在右侧选择目标系统账户，再创建策略路由'), '管理员创建策略路由前必须先选择具体系统账户')
assert(routeStrategiesViewSource.includes('routeStrategyOperationScopeParams(record)'), '策略路由编辑和删除必须使用记录归属作用域')
assert(routeStrategiesViewSource.includes('systemAccountId: operationScopeParams?.systemAccountId'), '策略路由分组选项必须按当前操作系统账户加载')
assert(routeStrategiesViewSource.includes('groupOptionsRequestToken'), '策略路由分组选项加载必须防止编辑回填、打开下拉和远程搜索请求互相覆盖')
assert(routeStrategiesViewSource.includes('loadGroupOptionsResource'), '策略路由分组选项必须复用统一持久资源适配器')
assert(routeStrategiesViewSource.includes('selectedOptions: groupOptionsRaw.value'), '策略路由编辑详情已携带的分组元数据必须直接参与候选合并')
assert(routeStrategiesViewSource.includes('groupOptionsLoadingKey === requestKey'), '策略路由分组选项相同请求进行中时必须复用请求')
assert(routeStrategiesViewSource.includes('clearGroupOptionsSearchTimer'), '策略路由分组选项远程搜索必须防抖并在页面卸载时清理')
assert(!routeStrategiesViewSource.includes('ids: selectedIds'), '策略路由分组选项主窗口请求不能用已选 ID 过滤，否则编辑时下拉只剩当前分组')
assert(groupOptionsResourceSource.includes('missingIds'), '策略路由分组选项应在主窗口请求后单独补齐缺失的已选分组')
assert(groupOptionsResourceSource.includes('routeStrategyOptions'), '策略路由分组选项必须调用专用最小 options 契约')
assert(!groupOptionsResourceSource.includes('manageableOnly'), '策略路由分组选项不得排除当前调用方可使用的授权分组')
assert(!groupOptionsResourceSource.includes("purpose: 'account'"), '策略路由分组选项不得复用账户表单宽 DTO')
assert(routeStrategiesViewSource.includes('form.groupBindings'), '策略路由表单必须维护分组绑定行')
assert(routeStrategiesViewSource.includes('groupBindings = form.groupBindings.map'), '策略路由保存必须归一化分组绑定 payload')
assert(routeStrategiesViewSource.includes('请选择分组'), '策略路由表单必须校验分组不能为空')
assert(!routeStrategiesViewSource.includes('v-model:value="binding.priority"'), '混合智能、故障回退和轮询路由不得展示分组优先级或顺序输入')
assert(routeStrategiesViewSource.includes("bindingShowsDragHandle = computed(() => form.mode === 'hybrid_smart' || form.mode === 'failover' || form.mode === 'round_robin')"), '混合智能、故障回退和轮询路由必须通过拖拽手柄调整分组顺序')
assert(routeStrategiesViewSource.includes("bindingOrderUsesPosition = computed(() => form.mode === 'hybrid_smart' || form.mode === 'failover' || form.mode === 'round_robin')"), '混合智能、故障回退和轮询路由必须按当前行顺序生成隐藏优先级')
assert(routeStrategiesViewSource.includes('priority: bindingOrderUsesPosition.value ? index + 1 : binding.priority'), '隐藏优先级路由保存必须按分组添加顺序生成 priority')
assert(routeStrategiesViewSource.includes('minimumBindingRowsForMode'), '策略路由表单必须按路由模式维护最少分组行数')
assert(routeStrategiesViewSource.includes("mode === 'weighted' || mode === 'round_robin' || mode === 'failover' ? 2 : 1"), '权重、轮询和故障回退路由表单必须至少展示两个分组行')
assert(routeStrategiesViewSource.includes('bindingRemoveDisabled'), '策略路由分组删除按钮必须按当前模式最少行数禁用')
assert(routeStrategiesViewSource.includes('bindingRowDragEnabled(index)'), '故障回退路由必须按行控制拖拽范围')
assert(routeStrategiesViewSource.includes("form.mode === 'hybrid_smart' || form.mode === 'round_robin'"), '轮询路由所有分组都必须允许拖拽排序')
assert(routeStrategiesViewSource.includes("if (form.mode === 'failover') return index >= 0 && index < form.groupBindings.length && form.groupBindings.length > 1"), '故障回退路由必须允许所有分组行拖拽排序，包括只有一个备用分组时')
assert(routeStrategiesViewSource.includes('Math.max(0, index)'), '故障回退路由必须允许把备用分组拖到第一行晋升为主用')
assert(routeStrategiesViewSource.includes('bindingRoleText(index)'), '故障回退路由必须展示主用和备用角色')
assert(routeStrategiesViewSource.includes('添加备用分组'), '故障回退路由添加入口必须表达备用分组语义')
assert(routeStrategiesViewSource.includes('故障回退路由的主用分组必须启用'), '故障回退路由必须校验主用分组启用')
assert(routeStrategiesViewSource.includes('故障回退路由至少需要一个启用备用分组'), '故障回退路由必须校验至少一个启用备用分组')
assert(routeStrategiesViewSource.includes('HolderOutlined'), '隐藏优先级路由分组排序必须展示拖拽图标')
assert(routeStrategiesViewSource.includes('v-model:value="binding.weight"'), '策略路由分组绑定必须维护权重')
assert(routeStrategiesViewSource.includes(':max="bindingWeightMax(index)"'), '权重调度路由必须按其他分组权重动态限制单项最大值')
assert(routeStrategiesViewSource.includes('weightedBindingTotal.value >= 100'), '权重调度路由总权重达到 100 后必须禁止继续添加分组')
assert(routeStrategiesViewSource.includes('权重调度路由的分组权重总和不能超过 100'), '权重调度路由保存前必须校验总权重不超过 100')
assert(routeStrategiesViewSource.includes('status: \'active\' | \'disabled\''), '策略路由分组绑定必须支持启停状态')
assert(routeStrategiesViewSource.includes('v-if="form.normal.schedulingPreference === \'speed_first\'" label="首字截止"'), '普通路由表单必须仅为速度优先展示首字截止')
assert(routeStrategiesViewSource.includes("return { schedulingPreference: 'cost_first' }"), '成本优先 payload 不得写入首字截止字段')
assert(!routeStrategiesViewSource.includes('firstByteThresholdMs'), '策略路由前端不得继续把旧速度模式首字阈值作为事实字段')
assert(accessTypesSource.includes('firstByteDeadlineMs: number'), '前端领域类型必须声明公共首字截止字段')
assert(!accessTypesSource.includes('firstByteThresholdMs'), '前端领域类型不得声明旧速度模式首字阈值字段')

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

async function assertRouteStrategySelectedOptionMerge(): Promise<void> {
  const selected = routeStrategyOption('selected-route')
  const windowOption = routeStrategyOption('window-route')
  const requests: Array<{ ids?: string[]; keyword?: string; limit?: number; activeOnly?: boolean; systemAccountId?: string }> = []
  let applied: RouteStrategyOptionSummary[] = []
  const result = await loadRouteStrategyOptionsResource({
    api: {
      options: async (params) => {
        requests.push(params ?? {})
        return params?.ids?.length ? [selected] : [windowOption]
      }
    },
    apply: (options) => {
      applied = options
    },
    isManagementView: false,
    selectedIds: [selected.id]
  })

  assert.deepEqual(requests, [
    { keyword: undefined, limit: 50, activeOnly: false, systemAccountId: undefined },
    { ids: [selected.id], limit: 1, activeOnly: false, systemAccountId: undefined }
  ], '策略路由选项必须先加载候选窗口，再只补齐缺失的已选项')
  assert.deepEqual(result.map((item) => item.id), [selected.id, windowOption.id], '策略路由选项必须合并已选项和完整候选窗口')
  assert.deepEqual(applied, result, '策略路由选项必须把合并结果交给页面')
}

async function assertGroupSelectedOptionMerge(): Promise<void> {
  const selected = groupOption('selected-group')
  const windowOption = groupOption('window-group')
  const requests: Array<{ ids?: string[]; keyword?: string; limit?: number; providerCode?: string; systemAccountId?: string }> = []
  let applied: RouteStrategyGroupOption[] = []
  const result = await loadGroupOptionsResource({
    api: {
      routeStrategyOptions: async (params) => {
        requests.push(params ?? {})
        return params?.ids?.length ? [selected] : [windowOption]
      }
    },
    apply: (groups) => {
      applied = groups
    },
    isManagementView: false,
    selectedIds: [selected.id]
  })

  assert.deepEqual(requests, [
    { keyword: undefined, limit: 50, systemAccountId: undefined },
    { ids: [selected.id], limit: 1, systemAccountId: undefined }
  ], '分组选项必须先加载候选窗口，再只补齐本地未知的已选项')
  assert.deepEqual(result.map((item) => item.id), [selected.id, windowOption.id], '分组选项必须合并已选项和完整候选窗口')
  assert.deepEqual(applied, result, '分组选项必须把合并结果交给页面')
}

async function assertKnownGroupSelectedOptionMerge(): Promise<void> {
  const selected = groupOption('known-selected-group')
  const windowOption = groupOption('known-window-group')
  const requests: Array<{ ids?: string[]; keyword?: string; limit?: number; providerCode?: string; systemAccountId?: string }> = []
  const result = await loadGroupOptionsResource({
    api: {
      routeStrategyOptions: async (params) => {
        requests.push(params ?? {})
        return [windowOption]
      }
    },
    apply: () => undefined,
    isManagementView: false,
    selectedIds: [selected.id],
    selectedOptions: [selected]
  })

  assert.deepEqual(requests, [
    { keyword: undefined, limit: 50, systemAccountId: undefined }
  ], '编辑详情已携带已选分组元数据时不得重复按 ID 请求')
  assert.deepEqual(result.map((item) => item.id), [selected.id, windowOption.id], '本地已选分组必须与远程候选窗口合并')
}

function routeStrategyOption(id: string): RouteStrategyOptionSummary {
  return {
    id,
    name: id,
    mode: 'normal',
    status: 'active',
    isDefault: false
  }
}

function groupOption(id: string): RouteStrategyGroupOption {
  return {
    id,
    name: id,
    providerCode: 'gpt',
    enabled: true
  }
}
