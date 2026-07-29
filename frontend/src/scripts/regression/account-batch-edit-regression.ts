import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { api } from '../../api/client'
import {
  accountBatchEditContextFieldsForForm,
  accountBatchEditFieldLabels,
  buildAccountBatchEditRequest,
  createAccountBatchEditForm,
  intersectAccountSupportedEndpointModes
} from '../../views/accounts/accountBatchEditForm'
import { loadAccountProviderModelOptionsResource } from '../../views/accounts/useAccountProviderModelOptions'
import type { AccountBatchEditContextItem, AccountSupportedEndpointMode } from '../../types/domain'

const accounts = [
  accountFixture('account_batch_frontend_a', 3),
  accountFixture('account_batch_frontend_b', 7)
]
const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../../..')
const form = createAccountBatchEditForm()
form.enabled.tags = true
form.tags = ['生产', ' 生产 ', '回归']
form.enabled.proxyProfileId = true
form.proxyProfileId = undefined
form.enabled.supportedModels = true
form.supportedModels = ['gpt-5.5', 'gpt-5.4', 'gpt-5.5']
form.enabled.healthCheckModel = true
form.healthCheckModel = 'gpt-5.4'
form.enabled.serviceTierOverride = true
form.serviceTierOverride = ''

const result = buildAccountBatchEditRequest(accounts, form)
assert.ok(result.payload, result.message)
assert.deepEqual(result.payload?.targets, [
  { accountId: accounts[0].id, configRevision: 3 },
  { accountId: accounts[1].id, configRevision: 7 }
], '批量编辑必须携带每个账户的最新配置版本')
assert.deepEqual(result.payload?.updates.tags, { enabled: true, value: ['生产', '回归'] }, '标签应去重后直接覆盖')
assert.deepEqual(result.payload?.updates.proxyProfileId, { enabled: true, value: null }, '清空代理必须和未勾选区分')
assert.deepEqual(
  result.payload?.updates.supportedModels,
  { enabled: true, value: ['gpt-5.5', 'gpt-5.4'] },
  '支持模型应去重后直接覆盖'
)
assert.deepEqual(result.payload?.updates.healthCheckModel, { enabled: true, value: 'gpt-5.4' }, '检查模型应单独提交')
assert.deepEqual(result.payload?.updates.serviceTierOverride, { enabled: true, value: '' }, '空 GPT 覆盖表示明确清除')
assert.equal(result.payload?.updates.priority, undefined, '未勾选字段不得进入请求')

const invalidHealthForm = createAccountBatchEditForm()
invalidHealthForm.enabled.supportedModels = true
invalidHealthForm.supportedModels = ['gpt-5.5']
invalidHealthForm.enabled.healthCheckModel = true
invalidHealthForm.healthCheckModel = 'gpt-5.4'
assert.equal(
  buildAccountBatchEditRequest(accounts, invalidHealthForm).message,
  '检查模型必须属于本次覆盖的支持模型',
  '支持模型与检查模型必须按最终快照校验'
)

const differingEndpointModeAccounts = [
  accountFixture('account_batch_frontend_chat', 11, ['chat_sse']),
  accountFixture('account_batch_frontend_responses', 13, ['responses_json'])
]
assert.deepEqual(
  intersectAccountSupportedEndpointModes(differingEndpointModeAccounts),
  [],
  '所选同构账户的目标能力上下文必须取全部账户交集'
)
const conflictingMappingForm = createAccountBatchEditForm()
conflictingMappingForm.enabled.modelMappings = true
conflictingMappingForm.modelMappings = [{
  sourceModel: 'gpt-source',
  sourceEndpointFamily: 'responses',
  upstreamModel: 'gpt-5.5',
  upstreamEndpointFamily: 'chat_completions',
  enabled: true
}]
assert.match(
  buildAccountBatchEditRequest(differingEndpointModeAccounts, conflictingMappingForm).message ?? '',
  /Chat Completions.*上游接口能力/,
  '未覆盖接口能力时，批量映射必须按全部所选账户能力交集校验，不能只看首账户'
)

const disabledConflictingMappingForm = createAccountBatchEditForm()
disabledConflictingMappingForm.enabled.modelMappings = true
disabledConflictingMappingForm.modelMappings = conflictingMappingForm.modelMappings.map((mapping) => ({ ...mapping, enabled: false }))
assert.ok(
  buildAccountBatchEditRequest(differingEndpointModeAccounts, disabledConflictingMappingForm).payload,
  '停用映射应允许在目标族能力交集为空时原样提交'
)

const overwrittenEndpointModeForm = createAccountBatchEditForm()
overwrittenEndpointModeForm.enabled.modelMappings = true
overwrittenEndpointModeForm.modelMappings = conflictingMappingForm.modelMappings.map((mapping) => ({ ...mapping }))
overwrittenEndpointModeForm.enabled.supportedEndpointModes = true
overwrittenEndpointModeForm.supportedEndpointModes = ['chat_json']
assert.ok(
  buildAccountBatchEditRequest(differingEndpointModeAccounts, overwrittenEndpointModeForm).payload,
  '显式覆盖接口能力时，批量映射必须使用表单中的目标能力校验'
)

const structurallyInvalidDisabledMappingForm = createAccountBatchEditForm()
structurallyInvalidDisabledMappingForm.enabled.modelMappings = true
structurallyInvalidDisabledMappingForm.modelMappings = [{
  sourceModel: 'gpt-source',
  sourceEndpointFamily: 'chat_completions',
  upstreamModel: 'gpt-5.5',
  upstreamEndpointFamily: 'responses',
  enabled: false
}]
assert.match(
  buildAccountBatchEditRequest(differingEndpointModeAccounts, structurallyInvalidDisabledMappingForm).message ?? '',
  /账号模型别名只支持同协议映射/,
  '停用映射只豁免目标能力冲突，不能豁免 provider/profile 转换结构冲突'
)

const missingVersionAccounts = accounts.map((account) => ({ ...account, configRevision: undefined }))
const noVersionForm = createAccountBatchEditForm()
noVersionForm.enabled.notes = true
noVersionForm.notes = '批量备注'
assert.match(
  buildAccountBatchEditRequest(missingVersionAccounts, noVersionForm).message ?? '',
  /版本信息缺失/,
  '账户版本缺失时不得提交覆盖'
)

const modalSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountBatchEditModal.vue'), 'utf8')
const formSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/accountBatchEditForm.ts'), 'utf8')
const accountsViewSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountsView.vue'), 'utf8')
const accountApiSource = readFileSync(resolve(frontendRoot, 'src/api/domains/accounts.ts'), 'utf8')
const accountMetaFieldsSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountMetaFields.vue'), 'utf8')
const accountScheduleSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountAvailabilityScheduleSection.vue'), 'utf8')
const accountStrategySource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountStrategySection.vue'), 'utf8')
const accountGptOverridesSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/AccountGptRequestOverridesSection.vue'), 'utf8')
const generalTabSource = modalSource.match(/<a-tab-pane key="general"[\s\S]*?<a-tab-pane key="rules"/)?.[0] ?? ''
const modelsTabSource = modalSource.match(/<a-tab-pane key="models"[\s\S]*?<\/a-tab-pane>/)?.[0] ?? ''
assert.match(modalSource, /batchEditContext\(/, '批量编辑应提供字段级去敏上下文读取')
assert.match(modalSource, /loadedModelContextFields/, '批量编辑必须记录已经按需加载的模型上下文字段')
assert.doesNotMatch(formSource, /account\.credentials/, '批量编辑表单不得依赖完整 credentials')
assert.match(modalSource, /label="上游接口能力"/, '批量编辑必须使用上游接口能力标签')
assert.match(modalSource, /覆盖账户真实上游支持的接口形态/, '批量编辑说明必须表达真实上游能力')
assert.doesNotMatch(modalSource, /接口能力限制|可承接的请求形态/, '批量编辑不得继续展示旧能力文案')
assert.equal(accountBatchEditFieldLabels.supportedEndpointModes, '上游接口能力', '批量字段摘要必须使用上游接口能力')
assert.equal(accountBatchEditFieldLabels.healthCheckEndpointMode, '检查请求形态', '批量确认摘要必须使用检查请求形态')
assert.doesNotMatch(generalTabSource, /form\.enabled\.healthCheckEndpointMode/, '检查请求形态不得继续放在通用配置页签')
assert.match(
  modelsTabSource,
  /class="batch-edit-two-columns"[\s\S]*form\.enabled\.healthCheckModel[\s\S]*form\.enabled\.healthCheckEndpointMode/,
  '检查模型与检查请求形态必须在模型与协议页签同一行，并分别保留覆盖复选框'
)
assert.deepEqual({
  tags: accountBatchEditFieldLabels.tags,
  availabilitySchedule: accountBatchEditFieldLabels.availabilitySchedule,
  notes: accountBatchEditFieldLabels.notes,
  modelMappings: accountBatchEditFieldLabels.modelMappings,
  serviceTierOverride: accountBatchEditFieldLabels.serviceTierOverride,
  reasoningEffortOverride: accountBatchEditFieldLabels.reasoningEffortOverride
}, {
  tags: '账户标签',
  availabilitySchedule: '时间计划',
  notes: '说明',
  modelMappings: '账号模型别名',
  serviceTierOverride: '服务等级',
  reasoningEffortOverride: '思考级别'
}, '批量字段标签和确认摘要必须使用单编辑同款中文文案')
for (const [source, expectedText] of [
  [accountMetaFieldsSource, 'label="账户标签"'],
  [accountMetaFieldsSource, 'label="说明"'],
  [accountScheduleSource, 'label="时间计划"'],
  [accountStrategySource, 'label="账号模型别名"'],
  [accountStrategySource, '新增别名'],
  [accountStrategySource, 'placeholder="客户端模型"'],
  [accountStrategySource, 'placeholder="上游模型"'],
  [accountGptOverridesSource, 'label="服务等级"'],
  [accountGptOverridesSource, 'label="思考级别"']
] as const) {
  assert.ok(source.includes(expectedText), `单编辑必须保留统一文案：${expectedText}`)
  assert.ok(modalSource.includes(expectedText), `批量编辑必须使用统一文案：${expectedText}`)
}
assert.match(modalSource, /isAccountModelMappingSourceEndpointFamilyAllowed/, '批量来源协议选项必须复用结构矩阵过滤')
assert.match(modalSource, /mappingSourceModelOptionsFor\(mapping\)/, '批量客户端模型必须复用单编辑来源模型策略')
assert.match(modalSource, /mappingUpstreamModelOptionsFor\(mapping\)/, '批量上游模型必须按每行上游协议过滤')
assert.match(modalSource, /enabled: mapping\.enabled/, '批量目标协议选项必须传入每条映射启停状态')
assert.match(modalSource, /intersectAccountSupportedEndpointModes\(accountDetails\.value\)/, '批量目标协议选项必须使用全部账户能力交集')
assert.doesNotMatch(modalSource, /advancedDetail\(/, '批量编辑不得逐账户读取高级详情')
const modalOpenWatchSource = modalSource.slice(
  modalSource.indexOf('watch(open,'),
  modalSource.indexOf('async function loadContext')
)
assert.doesNotMatch(modalOpenWatchSource, /loadModelOptions\(/, '打开批量编辑弹窗不得预取模型候选')
const initialContextSource = modalSource.slice(
  modalSource.indexOf('async function loadContext'),
  modalSource.indexOf('async function ensureModelContext')
)
assert.doesNotMatch(initialContextSource, /batchEditContext\(/, '打开批量编辑弹窗不得读取支持模型、模型映射或接口能力')
assert.match(initialContextSource, /props\.accounts\.map\(accountBatchEditContextFromListItem\)/, '首开必须直接复用列表行的 ID、版本和协议身份')
const deferredContextSource = modalSource.slice(
  modalSource.indexOf('async function ensureModelContext'),
  modalSource.indexOf('async function loadModelOptions')
)
assert.match(deferredContextSource, /batchEditContext\(accountIds, requestedFields/, '用户启用模型字段后必须只读取该交互依赖的上下文字段')
assert.match(deferredContextSource, /if \(token !== loadToken \|\| !open\.value\) return[\s\S]*catch \(error\) \{[\s\S]*if \(token !== loadToken \|\| !open\.value\) return/, '模型上下文成功和失败响应都必须隔离关闭或重开后的旧请求')
assert.match(deferredContextSource, /revisionChanged[\s\S]*pendingModelContextFields\.add\(field\)[\s\S]*clearAccountModelContext/, '分批上下文遇到版本变化时必须丢弃旧字段并重取，不能混合不同版本快照')
const contextFieldPlannerSource = modalSource.slice(
  modalSource.indexOf('function modelContextFieldsForEnabledForm'),
  modalSource.indexOf('function accountBatchEditContextFromListItem')
)
assert.match(contextFieldPlannerSource, /accountBatchEditContextFieldsForForm\(form, homogeneousAccount\.value\?\.providerCode\)/, '批量弹窗必须复用可执行的字段级上下文规划器')
verifyModelContextFieldPlanning()
const requestOverrideVisibilitySource = modalSource.slice(
  modalSource.indexOf('const requestOverridesSupported'),
  modalSource.indexOf('const modelConfigurationLoading')
)
assert.match(requestOverrideVisibilitySource, /loadedModelContextFields\.has\('supportedEndpointModes'\)[\s\S]*: undefined/, 'Gemini 接口能力尚未加载时必须保留请求覆盖入口')
assert.match(requestOverrideVisibilitySource, /\|\| form\.enabled\.serviceTierOverride[\s\S]*\|\| form\.enabled\.reasoningEffortOverride/, '接口能力加载后即使不支持，也必须保留已启用字段入口供用户取消，不能隐藏仍会提交的值')
assert.match(
  modalSource.slice(
    modalSource.indexOf('function handleMappingModelOptionsOpen'),
    modalSource.indexOf('function handleMappingModelOptionsSearch')
  ),
  /ensureModelContext\(modelContextFieldsForEnabledForm\(\)\)[\s\S]*loadModelOptions\(token\)/,
  '批量编辑模型候选必须仅在用户展开模型下拉后加载'
)
const supportedModelSelectSource = modelsTabSource.match(/v-model:value="form\.supportedModels"[\s\S]*?\/>/)?.[0] ?? ''
assert.match(supportedModelSelectSource, /:filter-option="false"/, '支持模型必须使用服务端搜索，不能只过滤首批 50 条')
assert.match(supportedModelSelectSource, /@search="handleSupportedModelOptionsSearch"/, '支持模型输入搜索必须请求供应商模型目录')
for (const [start, end, label] of [
  ['function handleMappingModelOptionsOpen', 'function handleMappingModelOptionsSearch', '映射下拉'],
  ['function scheduleModelOptionsSearch', 'function clearModelOptionsSearchTimer', '模型搜索'],
  ['() => [form.enabled.serviceTierOverride', 'async function save', '请求覆盖字段']
] as const) {
  const asynchronousModelLoadSource = modalSource.slice(modalSource.indexOf(start), modalSource.indexOf(end, modalSource.indexOf(start)))
  assert.match(asynchronousModelLoadSource, /const token = loadToken[\s\S]*token === loadToken && open\.value[\s\S]*loadModelOptions\(token/, `${label}等待上下文期间关闭或重开后不得替新弹窗加载模型目录`)
}
const saveSource = modalSource.slice(
  modalSource.indexOf('async function save'),
  modalSource.indexOf('function close')
)
assert.match(saveSource, /const token = loadToken[\s\S]*await ensureModelContext[\s\S]*token !== loadToken \|\| !open\.value/, '保存等待模型上下文期间关闭或重开弹窗后不得继续提交旧表单')
assert.match(formSource, /configRevision/, '批量编辑请求必须使用乐观版本')
assert.match(accountsViewSource, /@edit="openBatchEdit"/, '账户列表批量工具栏应接入批量编辑入口')
assert.match(accountsViewSource, /AccountBatchDisableConfirmModal/, '批量停用必须使用独立二次确认弹窗')
assert.match(accountsViewSource, /openBatchDisableConfirm/, '批量停用按钮不得直接执行状态更新')
assert.match(accountsViewSource, /AccountBatchDeleteConfirmModal/, '批量删除必须继续使用独立二次确认弹窗')
assert.match(accountApiSource, /batchUpdate:/, '管理侧和用户侧账户 API 应提供批量更新方法')
assert.match(accountApiSource, /batchEditContext:/, '管理侧和用户侧账户 API 应提供批量编辑上下文方法')
assert.doesNotMatch(accountsViewSource, /batchTestSelected|openBatchTestModal/, '账户列表不得恢复批量测试入口')

await verifySupportedModelSearchBeyondFirstPage()

console.log('账户批量编辑前端回归通过：显式覆盖、清空语义、版本校验和按需详情加载符合契约')

async function verifySupportedModelSearchBeyondFirstPage(): Promise<void> {
  const originalModelOptions = api.providers.modelOptions
  const catalog = Array.from({ length: 75 }, (_item, index) => {
    const id = `catalog-model-${String(index + 1).padStart(3, '0')}`
    return {
      id,
      name: id,
      supportedApiProtocols: ['responses'] as const,
      supportedServiceTiers: [],
      supportedReasoningEfforts: []
    }
  })
  const queries: Array<{ keyword?: string; limit?: number }> = []
  try {
    api.providers.modelOptions = async (params) => {
      queries.push({ keyword: params?.keyword, limit: params?.limit })
      const keyword = params?.keyword?.trim().toLowerCase()
      return catalog
        .filter((item) => !keyword || item.id.includes(keyword))
        .slice(0, params?.limit ?? 50)
    }
    const initial = await loadAccountProviderModelOptionsResource({
      isManagementView: false,
      providerCode: 'gpt'
    })
    assert.equal(initial.data.length, 50, '首次展开只应读取前 50 个模型候选')
    assert.equal(initial.data.some((item) => item.value === 'catalog-model-075'), false, '第 75 个模型不应被首批响应提前加载')

    const searched = await loadAccountProviderModelOptionsResource({
      isManagementView: false,
      providerCode: 'gpt',
      keyword: 'catalog-model-075'
    })
    assert.deepEqual(searched.data.map((item) => item.value), ['catalog-model-075'], '服务端搜索必须能够取到首批 50 条以后的模型')
    assert.deepEqual(queries, [
      { keyword: undefined, limit: 50 },
      { keyword: 'catalog-model-075', limit: 50 }
    ], '批量编辑模型目录必须保持 50 条窗口并把搜索词发送到服务端')
  } finally {
    api.providers.modelOptions = originalModelOptions
  }
}

function verifyModelContextFieldPlanning(): void {
  const contextFieldsFor = (
    enabledFields: Array<keyof ReturnType<typeof createAccountBatchEditForm>['enabled']>,
    providerCode = 'gpt'
  ) => {
    const candidate = createAccountBatchEditForm()
    for (const field of enabledFields) candidate.enabled[field] = true
    return accountBatchEditContextFieldsForForm(candidate, providerCode)
  }

  assert.deepEqual(contextFieldsFor([]), [], '首开没有启用模型字段时不得请求任何批量模型上下文')
  assert.deepEqual(contextFieldsFor(['supportedModels']), [], '覆盖支持模型不得读取旧支持模型关系')
  assert.deepEqual(contextFieldsFor(['healthCheckModel']), ['supportedModels'], '只覆盖检查模型时只能读取现有支持模型')
  assert.deepEqual(contextFieldsFor(['healthCheckEndpointMode']), ['supportedEndpointModes'], '只覆盖检查请求形态时只能读取现有接口能力')
  assert.deepEqual(contextFieldsFor(['supportedEndpointModes']), ['modelMappings'], '只覆盖接口能力时只能读取现有映射用于冲突校验')
  assert.deepEqual(
    contextFieldsFor(['modelMappings']),
    ['supportedModels', 'supportedEndpointModes'],
    '只覆盖模型别名时只能读取其两个现有校验依赖'
  )
  assert.deepEqual(
    contextFieldsFor(['supportedModels', 'supportedEndpointModes', 'modelMappings']),
    [],
    '三个模型字段都显式覆盖时不得读取任何旧模型上下文'
  )
  assert.deepEqual(contextFieldsFor(['serviceTierOverride']), ['supportedModels'], 'GPT 服务等级只依赖现有支持模型')
  assert.deepEqual(
    contextFieldsFor(['reasoningEffortOverride'], 'gemini'),
    ['supportedModels', 'supportedEndpointModes'],
    'Gemini 思考级别必须定点补取支持模型和接口能力'
  )
}

function accountFixture(
  id: string,
  configRevision: number,
  supportedEndpointModes: AccountSupportedEndpointMode[] = ['chat_sse']
): AccountBatchEditContextItem {
  return {
    id,
    configRevision,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    supportedModels: ['gpt-5.5', 'gpt-5.4'],
    modelMappings: [],
    supportedEndpointModes
  }
}
