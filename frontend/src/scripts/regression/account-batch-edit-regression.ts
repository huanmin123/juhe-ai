import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  accountBatchEditFieldLabels,
  buildAccountBatchEditRequest,
  createAccountBatchEditForm,
  intersectAccountSupportedEndpointModes
} from '../../views/accounts/accountBatchEditForm'
import type { AccountSummary } from '../../types/domain'

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
assert.match(modalSource, /batchEditContext\(/, '批量编辑应在打开弹窗后一次性按需读取去敏上下文')
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
  [accountStrategySource, 'placeholder="目标模型"'],
  [accountGptOverridesSource, 'label="服务等级"'],
  [accountGptOverridesSource, 'label="思考级别"']
] as const) {
  assert.ok(source.includes(expectedText), `单编辑必须保留统一文案：${expectedText}`)
  assert.ok(modalSource.includes(expectedText), `批量编辑必须使用统一文案：${expectedText}`)
}
assert.match(modalSource, /isAccountModelMappingSourceEndpointFamilyAllowed/, '批量来源协议选项必须复用结构矩阵过滤')
assert.match(modalSource, /enabled: mapping\.enabled/, '批量目标协议选项必须传入每条映射启停状态')
assert.match(modalSource, /intersectAccountSupportedEndpointModes\(accountDetails\.value\)/, '批量目标协议选项必须使用全部账户能力交集')
assert.doesNotMatch(modalSource, /advancedDetail\(/, '批量编辑不得逐账户读取高级详情')
assert.match(formSource, /configRevision/, '批量编辑请求必须使用乐观版本')
assert.match(accountsViewSource, /@edit="openBatchEdit"/, '账户列表批量工具栏应接入批量编辑入口')
assert.match(accountsViewSource, /AccountBatchDisableConfirmModal/, '批量停用必须使用独立二次确认弹窗')
assert.match(accountsViewSource, /openBatchDisableConfirm/, '批量停用按钮不得直接执行状态更新')
assert.match(accountsViewSource, /AccountBatchDeleteConfirmModal/, '批量删除必须继续使用独立二次确认弹窗')
assert.match(accountApiSource, /batchUpdate:/, '管理侧和用户侧账户 API 应提供批量更新方法')
assert.match(accountApiSource, /batchEditContext:/, '管理侧和用户侧账户 API 应提供批量编辑上下文方法')
assert.doesNotMatch(accountsViewSource, /batchTestSelected|openBatchTestModal/, '账户列表不得恢复批量测试入口')

console.log('账户批量编辑前端回归通过：显式覆盖、清空语义、版本校验和按需详情加载符合契约')

function accountFixture(
  id: string,
  configRevision: number,
  supportedEndpointModes: AccountSummary['credentials']['supported_endpoint_modes'] = ['chat_sse']
): AccountSummary {
  return {
    id,
    configRevision,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: id,
    type: 'api_key',
    credentials: { supported_endpoint_modes: supportedEndpointModes },
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-5.5', 'gpt-5.4'],
    healthCheckModel: 'gpt-5.5',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage()
  }
}

function emptyUsage() {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}
