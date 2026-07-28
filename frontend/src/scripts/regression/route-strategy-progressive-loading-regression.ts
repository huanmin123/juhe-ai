import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import {
  buildRouteStrategyMutationPatch,
  hasRouteStrategyMutationChanges,
  mergeRouteStrategyMutationResult
} from '../../views/route-strategies/routeStrategyMutation'

const viewSource = readFileSync(fileURLToPath(new URL('../../views/route-strategies/RouteStrategiesView.vue', import.meta.url)), 'utf8')
const apiSource = readFileSync(fileURLToPath(new URL('../../api/domains/routeStrategies.ts', import.meta.url)), 'utf8')
const typesSource = readFileSync(fileURLToPath(new URL('../../types/domain/access.ts', import.meta.url)), 'utf8')
const modelOptionsSource = readFileSync(fileURLToPath(new URL('../../composables/useProviderModelSelectOptions.ts', import.meta.url)), 'utf8')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码片段起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码片段终点：${end}`)
  return source.slice(startIndex, endIndex)
}

assert.doesNotMatch(viewSource, /listSnapshot|routeStrategyListSnapshot|routeStrategySnapshotStatus|暂不可用|加载中/, '策略路由列表不得保留快照补发和动态占位')
assert.doesNotMatch(apiSource, /list-snapshot|listSnapshot/, '策略路由前端 API 不得保留独立列表快照入口')
assert.match(viewSource, /routeStrategiesApi\.list\(listParams\)/, '策略路由列表必须通过列表接口获取当前页完整数据')
assert.match(viewSource, /record\.bindingCount|record\.groupBindingPreview|record\.apiKeyCount/, '策略路由列表必须消费列表响应中的计数和预览')
assert.match(typesSource, /bindingCount: number[\s\S]*apiKeyCount: number[\s\S]*groupBindingPreview/, '策略路由列表类型必须声明完整动态字段')

const modeWatchSource = sourceBetween(viewSource, 'watch(() => form.mode', 'watch(snapshotPageState')
const openCreateSource = sourceBetween(viewSource, 'function openCreate()', 'async function openEdit')
const openEditSource = sourceBetween(viewSource, 'async function openEdit', 'function fillEditForm')
const fillEditFormSource = sourceBetween(viewSource, 'function fillEditForm', 'async function saveRouteStrategy')
const groupDropdownSource = sourceBetween(viewSource, 'function handleGroupOptionsDropdown', 'function handleGroupOptionsSearch')
const groupSearchSource = sourceBetween(viewSource, 'function handleGroupOptionsSearch', 'function clearGroupOptionsSearchTimer')
const groupLoadSource = sourceBetween(viewSource, 'async function loadGroupOptions', 'function groupOptionsRequestKey')
const modelDropdownSource = sourceBetween(viewSource, 'function handleModelOptionsDropdown', 'function handleModelOptionsSearch')
const modelSearchSource = sourceBetween(viewSource, 'function handleModelOptionsSearch', 'function normalizeBindingRowsForMode')
const saveSource = sourceBetween(viewSource, 'async function saveRouteStrategy', 'async function deleteRouteStrategy')

for (const [name, source] of [
  ['路由模式 watch', modeWatchSource],
  ['新增弹窗', openCreateSource],
  ['编辑弹窗', openEditSource],
  ['编辑表单填充', fillEditFormSource]
] as const) {
  assert.doesNotMatch(source, /loadGroupOptions\s*\(/, `${name}不得预取分组选项`)
  assert.doesNotMatch(source, /loadModelOptions\s*\(/, `${name}不得预取模型选项`)
}
assert.match(openEditSource, /routeStrategiesApi\.editBasicDetail\(/, '编辑弹窗必须只加载策略路由 edit-basic 投影')
assert.doesNotMatch(openEditSource, /routeStrategiesApi\.detail\(/, '编辑弹窗不得回退到完整策略路由详情')
assert.match(apiSource, /\/route-strategies\/\$\{id\}\/edit-basic/, '管理与个人策略路由 API 必须暴露 edit-basic 接口')
assert.match(typesSource, /interface RouteStrategyEditBasicDetail[\s\S]*groupBindings:/, '前端必须使用独立的策略路由编辑 DTO')
assert.match(openEditSource, /editDetailRequestSignature\(record\.id, operationScopeParams\?\.systemAccountId\)/, '编辑详情必须绑定记录与 owner 作用域签名')
assert.match(openEditSource, /isCurrentEditDetailRequest\(/, '编辑详情写回和错误提示必须校验当前请求上下文')
assert.match(openCreateSource, /resetRouteModelOptions\(\)/, '新增弹窗必须清除上一弹窗的模型候选和搜索定时器，且不得为此发请求')
assert.match(fillEditFormSource, /resetRouteModelOptions\(\)/, '编辑填充必须清除上一弹窗的模型候选和搜索定时器，且不得为此发请求')
assert.match(fillEditFormSource, /selectedGroupOptionsFromBindings\(record\.groupBindings\)/, '编辑弹窗必须用详情携带的已选分组标签和供应商初始化本地选项')
assert.match(viewSource, /const modelSelectOptions = computed[\s\S]*selectedModelIds\.value[\s\S]*label: value[\s\S]*loadedModelSelectOptions\.value/, '编辑弹窗必须保留详情携带的已选模型值和同名标签，且在候选加载后用权威选项覆盖')
assert.match(groupDropdownSource, /if \(open && !groupOptionsLoaded\.value\)[\s\S]*loadGroupOptions\(/, '分组候选只能由真实下拉展开按需加载')
assert.match(groupSearchSource, /loadGroupOptions\(value,/, '分组搜索仍必须按输入加载候选')
assert.match(groupLoadSource, /groupOptionsLoaded\.value = !keyword/, '关键词搜索结果不得冒充默认候选缓存')
assert.match(groupLoadSource, /currentSelectedIds[\s\S]*groupOptionsRaw\.value[\s\S]*!merged\.has\(group\.id\)/, '分组搜索响应必须保留请求期间新选中的分组元数据')
assert.match(modelDropdownSource, /if \(open && modelProviderCodes\.value\.length\)[\s\S]*loadModelOptions\(/, '模型候选只能由真实下拉展开按需加载')
assert.match(modelSearchSource, /loadModelOptions\(\{ keyword: value, selectedIds: selectedModelIds\.value \}\)/, '模型搜索仍必须按输入加载候选')
assert.match(modelSearchSource, /clearModelOptionsSearchTimer\(\)/, '模型搜索必须通过统一定时器清理入口调度')
assert.match(modeWatchSource, /clearModelOptionsSearchTimer\(\)/, '切换路由模式必须取消隐藏模型控件的待执行搜索')
assert.match(viewSource, /watch\(\(\) => modelProviderCodes\.value\.join\([\s\S]*clearModelOptionsSearchTimer\(\)[\s\S]*resetModelOptions\(\)/, '模型供应商作用域变化时必须立即清除旧供应商候选')
assert.match(viewSource, /watch\(modalOpen,[\s\S]*clearModelOptionsSearchTimer\(\)[\s\S]*resetModelOptions\(\)/, '关闭弹窗必须清除模型搜索定时器与远程候选')
assert.match(viewSource, /onBeforeUnmount\([\s\S]*clearModelOptionsSearchTimer\(\)[\s\S]*resetModelOptions\(\)/, '卸载页面必须作废模型搜索和在途候选请求')
assert.match(viewSource, /onBeforeUnmount\([\s\S]*invalidateEditDetailRequest\(\)/, '卸载页面必须作废在途编辑详情')
assert.match(viewSource, /function resetFilters\(\)[\s\S]*invalidateEditDetailRequest\(\)/, '重置筛选必须作废旧 owner 的编辑详情')
assert.match(viewSource, /function handleSystemAccountFilterChange\(\)[\s\S]*invalidateEditDetailRequest\(\)/, '切换系统账户必须作废旧 owner 的编辑详情')
assert.match(viewSource, /function handleSystemAccountFilterChange\(\)[\s\S]*resetRouteStrategyListForScopeChange\(\)[\s\S]*resetRouteModelOptions\(\)/, '切换系统账户必须立即清空旧列表和旧模型候选')
assert.match(viewSource, /onMissingSelectedIds: handleMissingSystemAccountFilter/, '远程确认 owner 失效后必须回退安全作用域')
assert.match(viewSource, /function handleMissingSystemAccountFilter[\s\S]*systemAccountFilter\.value = allSystemAccountsValue[\s\S]*resetRouteStrategyListForScopeChange\(\)[\s\S]*loadRouteStrategies\(\)/, '失效 owner 必须清空旧列表并重新加载全部作用域')
assert.match(viewSource, /function resetRouteStrategyListForScopeChange[\s\S]*routeStrategyListRequestGeneration \+= 1[\s\S]*items\.value = \[\][\s\S]*total\.value = 0/, 'owner 作用域变化时旧列表必须同步失效，失败时不得保留其他 owner 行')
assert.match(viewSource, /function editDetailRequestSignature[\s\S]*authState\.revision\.value[\s\S]*viewer\?\.id[\s\S]*viewer\?\.role[\s\S]*routeStrategyListScopeKey\(\)[\s\S]*systemAccountId[\s\S]*recordId/, '编辑详情签名必须覆盖身份、页面 scope、owner 和记录 ID')
assert.match(modelOptionsSource, /params\.force !== true && loadedScopeKey === scopeKey/, '相同作用域的模型下拉重复展开必须复用已加载候选')
assert.match(saveSource, /routeStrategiesApi\.update[\s\S]*if \(editingIsDefault\.value\)[\s\S]*invalidateUserReferenceData\([\s\S]*viewScope: isManagementView\.value \? 'admin' : 'self'[\s\S]*systemAccountId: operationScopeParams\?\.systemAccountId/, '默认路由更新后必须只失效对应用户 scope 的共享默认资源缓存')
assert.match(saveSource, /buildRouteStrategyMutationPatch\(editingBaseline, completePayload\)/, '策略路由编辑保存必须根据打开时基线生成字段级 PATCH')
assert.match(saveSource, /!hasRouteStrategyMutationChanges\(payload\)[\s\S]*没有需要保存的修改/, '策略路由未修改保存不得发出 PATCH')
const updateBranch = sourceBetween(saveSource, 'if (editingId.value) {', '} else {')
assert.match(updateBranch, /const result = await routeStrategiesApi\.update[\s\S]*applyRouteStrategyMutationResult\(result\)/, '编辑成功必须用最小 mutation result 本地合并列表行')
assert.doesNotMatch(updateBranch, /loadRouteStrategies\(/, '编辑成功不得重新加载策略路由列表')
const createBranch = sourceBetween(saveSource, '} else {\n      await routeStrategiesApi.create', 'message.success(\'策略路由已创建\')')
assert.doesNotMatch(createBranch, /invalidateUserReferenceData/, '普通路由创建不得无条件失效共享默认资源缓存')
const createCompletionBranch = sourceBetween(saveSource, '} else {\n      await routeStrategiesApi.create', '\n    }\n  } catch')
assert.match(createCompletionBranch, /await loadRouteStrategies\(\)/, '创建成功可以重新加载策略路由列表以接收服务端生成字段')

const baseline = {
  name: '基线路由',
  description: null,
  mode: 'normal' as const,
  status: 'active' as const,
  groupBindings: [{ groupId: 'group-1', priority: 1, weight: 1, status: 'active' as const }],
  normalRoutingConfig: { schedulingPreference: 'cost_first' as const, firstByteDeadlineMs: 10000 },
  hybridRoutingConfig: null
}
assert.deepEqual(buildRouteStrategyMutationPatch(baseline, { ...baseline }), {}, '未修改策略路由不得生成 PATCH 字段')
assert.deepEqual(buildRouteStrategyMutationPatch(baseline, { ...baseline, description: '仅修改说明' }), {
  description: '仅修改说明'
}, '策略路由编辑只应提交实际变化字段')
assert.equal(hasRouteStrategyMutationChanges({}), false, '空策略路由 PATCH 必须识别为 no-op')

const listItem = {
  id: 'route-1',
  name: '基线路由',
  description: '旧说明',
  mode: 'normal' as const,
  status: 'active' as const,
  isDefault: false,
  normalRoutingConfig: { schedulingPreference: 'cost_first' as const, firstByteDeadlineMs: 10000 },
  bindingCount: 1,
  apiKeyCount: 7,
  groupBindingPreview: [{
    id: 'binding-1',
    groupId: 'group-1',
    groupName: '旧分组',
    providerCode: 'gpt',
    status: 'active' as const,
    groupEnabled: true
  }],
  createdAt: '2026-07-28T00:00:00.000Z',
  updatedAt: '2026-07-28T00:00:00.000Z'
}
const mergedListItem = mergeRouteStrategyMutationResult(listItem, {
  id: listItem.id,
  changedFields: ['name', 'description', 'mode', 'status', 'normalRoutingConfig', 'groupBindings'],
  rowPatch: {
    name: '本地合并路由',
    description: null,
    mode: 'weighted',
    status: 'disabled',
    normalRoutingConfig: null,
    bindingCount: 2,
    groupBindingPreview: [{
      id: 'binding-2',
      groupId: 'group-2',
      groupName: '新分组',
      providerCode: 'gpt',
      status: 'active',
      groupEnabled: true
    }],
    updatedAt: '2026-07-28T01:00:00.000Z'
  }
})
assert.equal(mergedListItem.name, '本地合并路由')
assert.equal('description' in mergedListItem, false, '服务端返回 null 时本地行必须清除可选说明字段')
assert.equal('normalRoutingConfig' in mergedListItem, false, '服务端清除普通路由配置时本地行不得保留旧配置')
assert.equal(mergedListItem.bindingCount, 2)
assert.equal(mergedListItem.groupBindingPreview[0]?.groupId, 'group-2')
assert.equal(mergedListItem.apiKeyCount, listItem.apiKeyCount, '最小 mutation result 未涉及的列表字段必须原样保留')

console.log('策略路由渐进加载回归通过：编辑使用窄投影、字段级 PATCH 和本地行合并，只有创建成功才刷新列表')
