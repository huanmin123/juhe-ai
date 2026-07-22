import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { buildAccountDraftTestPayload } from '../../src/views/accounts/accountDraftTestPayload'
import { defaultAccountForm } from '../../src/views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../src/views/accounts/accountOptions'
import {
  buildAccountSavePayload,
  validateAccountSaveForm
} from '../../src/views/accounts/accountSavePayload'
import {
  createSavedAccountApiKeyRuntimeSnapshot,
  visibleSavedAccountApiKeyRuntimeDetails
} from '../../src/views/accounts/accountApiKeyRuntimeDisplay'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const accountsViewSource = readSource('src/views/accounts/AccountsView.vue')
const editModalSource = readSource('src/views/accounts/AccountEditModal.vue')
const editTestSource = readSource('src/views/accounts/useAccountEditTestAction.ts')
const saveFlowSource = readSource('src/views/accounts/useAccountEditSaveFlow.ts')
const testModalSource = readSource('src/views/accounts/useAccountTestModal.ts')
const rowActionsSource = readSource('src/views/accounts/accountRowActions.ts')
const rowActionsViewSource = readSource('src/views/accounts/AccountRowActions.vue')
const tableCellSource = readSource('src/views/accounts/AccountTableCell.vue')
const mobileCardSource = readSource('src/views/accounts/AccountMobileCard.vue')
const accountListSource = readSource('src/views/accounts/AccountList.vue')
const derivedStateSource = readSource('src/views/accounts/accountDerivedState.ts')
const packageJson = JSON.parse(readSource('package.json')) as { scripts?: Record<string, string> }

for (const legacyPath of [
  'src/views/accounts/AccountBindGroupModal.vue',
  'src/views/accounts/useAccountBindGroup.ts'
]) {
  assert.equal(existsSync(resolve(frontendRoot, legacyPath)), false, `${legacyPath} 已被当前编辑流程替代，不得继续保留`)
}
for (const [sourceName, source] of [
  ['AccountRowActions.vue', rowActionsViewSource],
  ['AccountTableCell.vue', tableCellSource],
  ['AccountMobileCard.vue', mobileCardSource],
  ['AccountList.vue', accountListSource]
] as const) {
  assert.doesNotMatch(source, /bind-group/, `${sourceName} 不得再声明或透传旧 bind-group UI event`)
}
assert.doesNotMatch(rowActionsSource, /groupName\?:/, 'accountRowActions action options 不得保留仅透传的 groupName')
assert.doesNotMatch(rowActionsViewSource, /groupName\?:|groupName:\s*props\.groupName/, 'AccountRowActions 不得继续传递 groupName action option')
assert.doesNotMatch(derivedStateSource, /bindGroupOptionsForAccount|bindGroupTip/, '旧 bind-group hook 专用 derived helper 必须删除')
assert.match(derivedStateSource, /export function defaultGroupForProvider/, '账户编辑默认分组 helper 必须保留')
assert.match(tableCellSource, /groupName:\s*\(accountId: string\)/, 'PC 账户表格必须保留 groupName 显示 prop')
assert.match(mobileCardSource, /groupName\?: string/, '手机账户卡片必须保留 groupName 显示 prop')
assert.match(accountListSource, /groupName:\s*\(accountId: string\)/, '账户列表必须保留 groupName 显示 prop')
assert.match(saveFlowSource, /api\.accounts\.bindGroup/, '管理侧账户编辑保存必须继续调用 accounts.bindGroup')
assert.match(saveFlowSource, /api\.myAccounts\.bindGroup/, '用户侧账户编辑保存必须继续调用 myAccounts.bindGroup')

assert.doesNotMatch(
  `${accountsViewSource}\n${saveFlowSource}\n${testModalSource}`,
  /SuccessfulDraftActivationTest|successfulDraftActivationTest|successfulSavedDraftUpdateTest|defaultTestModelSaveQueue/,
  '人工测试结果不得再生成账户激活或默认模型持久化状态'
)
assert.doesNotMatch(
  saveFlowSource,
  /activationTestTaskId|测试通过后再保存|测试成功后/,
  '新增和编辑保存不得依赖人工测试结果'
)
assert.match(
  editTestSource,
  /openSavedDraftTestModal/,
  '编辑表单测试应使用当前未保存表单快照'
)
assert.match(
  testModalSource,
  /useFixedTestModel\(model/,
  '新增和编辑表单测试必须固定使用表单检查模型'
)
assert.match(
  editModalSource,
  /@click="\$emit\('test'\)"/,
  '账户编辑弹窗应保留人工诊断入口'
)

const savedRuntimeResponse = {
  accountId: 'account-runtime-display',
  configRevision: 7,
  items: [
    { keyIndex: 0, keyFingerprintPrefix: 'fingerprint-a', keySuffix: 'aaa1', weight: 1, status: 'active' as const, failureCount: 0, consecutiveFailures: 0, successCount: 1 },
    { keyIndex: 1, keyFingerprintPrefix: 'fingerprint-b', keySuffix: 'bbb2', weight: 1, status: 'temporary_unavailable' as const, failureCount: 2, consecutiveFailures: 2, successCount: 0 }
  ]
}
const savedRuntimeSnapshot = createSavedAccountApiKeyRuntimeSnapshot({
  accountId: 'account-runtime-display',
  configRevision: 7,
  apiKeys: ['sk-saved-a', 'sk-saved-b'],
  response: savedRuntimeResponse
})
assert(savedRuntimeSnapshot, '相同账户和配置版本应接受保存态 Key 运行明细')
assert.equal(visibleSavedAccountApiKeyRuntimeDetails(savedRuntimeSnapshot, [' sk-saved-a ', 'sk-saved-b'])?.length, 2, '未修改保存 Key 时应展示运行状态')
assert.equal(visibleSavedAccountApiKeyRuntimeDetails(savedRuntimeSnapshot, ['sk-edited-a', 'sk-saved-b']), undefined, '编辑 Key 后必须隐藏旧运行状态')
assert.equal(visibleSavedAccountApiKeyRuntimeDetails(savedRuntimeSnapshot, ['sk-saved-a', '', 'sk-saved-b']), undefined, '新增 Key 输入行后必须隐藏旧运行状态')
assert.equal(visibleSavedAccountApiKeyRuntimeDetails(savedRuntimeSnapshot, ['sk-saved-a']), undefined, '删除 Key 后必须隐藏旧运行状态')
assert.equal(visibleSavedAccountApiKeyRuntimeDetails(savedRuntimeSnapshot, ['sk-saved-b', 'sk-saved-a']), undefined, '重排 Key 后必须隐藏旧运行状态，避免按 keyIndex 错贴')
assert.equal(createSavedAccountApiKeyRuntimeSnapshot({
  accountId: 'account-runtime-display',
  configRevision: 8,
  apiKeys: ['sk-saved-a', 'sk-saved-b'],
  response: savedRuntimeResponse
}), undefined, '配置版本不一致时不得接受并行返回的旧运行状态')

const form = defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
form.name = '检查模型保存回归账户'
form.groupId = 'group_health_check'
form.group = { id: form.groupId, name: '检查模型回归分组' }
form.apiKey = 'sk-regression-health-check'
form.apiKeys = [form.apiKey]
form.supportedModels = ['gpt-5.5', 'gpt-5.4']
form.healthCheckModel = 'gpt-5.4'

assert.equal(
  validateAccountSaveForm({
    form,
    errorPolicyRules: [],
    responseInspectionRules: [],
    mappingUpstreamModelOptions: form.supportedModels.map((model) => ({ label: model, value: model })),
    providers: FALLBACK_PROVIDERS
  }),
  undefined,
  '检查模型属于支持模型时账户应允许保存，不要求人工测试先成功'
)

const savePayload = buildAccountSavePayload({
  accounts: [],
  form,
  errorPolicyRules: [],
  responseInspectionRules: []
})
const draftPayload = buildAccountDraftTestPayload({
  accounts: [],
  form,
  errorPolicyRules: [],
  responseInspectionRules: [],
  mappingUpstreamModelOptions: form.supportedModels.map((model) => ({ label: model, value: model })),
  providers: FALLBACK_PROVIDERS
})
assert.equal(savePayload.healthCheckModel, 'gpt-5.4', '账户保存必须持久化表单检查模型')
assert.equal(draftPayload.healthCheckModel, 'gpt-5.4', '新增和编辑人工测试草稿必须携带表单检查模型')
assert.deepEqual(savePayload.supportedModels, ['gpt-5.5', 'gpt-5.4'], '账户保存必须保留支持模型')

form.healthCheckModel = 'gpt-unknown'
assert.equal(
  validateAccountSaveForm({
    form,
    errorPolicyRules: [],
    responseInspectionRules: [],
    mappingUpstreamModelOptions: form.supportedModels.map((model) => ({ label: model, value: model })),
    providers: FALLBACK_PROVIDERS
  }),
  '检查模型必须从账户支持模型中选择',
  '检查模型不属于支持模型时必须拒绝保存'
)

assert.equal(
  packageJson.scripts?.['test:account-edit-save-flow'],
  'pnpm --dir ../backend exec tsx --tsconfig ../frontend/tsconfig.json ../frontend/scripts/regression/account-edit-save-flow-regression.ts',
  '前端 package script 应暴露账户编辑保存流程回归'
)

console.log('账户编辑保存流程回归通过：检查模型持久化、固定人工测试模型和零激活副作用符合当前契约')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}
