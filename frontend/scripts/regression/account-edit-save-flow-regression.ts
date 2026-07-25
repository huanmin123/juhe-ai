import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
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
const apiKeySectionSource = readSource('src/views/accounts/AccountApiKeySection.vue')
const oauthSectionSource = readSource('src/views/accounts/AccountOAuthSection.vue')
const packageJson = JSON.parse(readSource('package.json')) as { scripts?: Record<string, string> }

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
assert.match(
  accountsViewSource,
  /function cancelAccountModelCatalogSync\(\): void[\s\S]*modelCatalogSyncController\?\.abort\(\)/,
  '关闭弹窗或变更目录同步输入时必须取消正在进行的上游模型同步'
)
assert.match(
  accountsViewSource,
  /function modelCatalogDiscoveryRequestKey\([\s\S]*?JSON\.stringify\(payload\.account\)/,
  '上游模型同步必须基于完整的当前账户连接草稿生成请求标识'
)
assert.match(
  accountsViewSource,
  /requestKey !== currentModelCatalogDiscoveryRequestKey\(\)/,
  '上游模型目录响应返回时必须确认代理、分组和凭据等连接草稿没有变更'
)
assert.match(
  accountsViewSource,
  /watch\(\s*\[[\s\S]*?currentModelCatalogDiscoveryRequestKey\s*\],\s*\(\) =>/,
  '代理、分组或 OAuth 凭据变更时必须取消正在进行的上游模型同步'
)
assert.match(
  accountsViewSource,
  /if \(!form\.healthCheckModel\.trim\(\) && result\.recommendedHealthCheckModel\) form\.healthCheckModel = result\.recommendedHealthCheckModel/,
  '同步上游目录只可在检查模型为空时采用推荐值，不能覆盖用户手动选择'
)
assert.match(
  accountsViewSource,
  /onBeforeUnmount\(cancelAccountModelCatalogSync\)/,
  '离开账户页面时必须清理上游模型同步请求与延迟任务'
)
for (const [sectionName, source] of [['API Key', apiKeySectionSource], ['OAuth', oauthSectionSource]] as const) {
  assert.doesNotMatch(source, /<a-form-item required tooltip=/, `${sectionName} 支持模型说明不得由表单标签尾部自动渲染`)
  assert.match(source, /<span>支持模型<\/span>\s*<a-tooltip[^>]*>\s*<QuestionCircleOutlined class="supported-models-help"/s, `${sectionName} 支持模型说明图标必须紧跟标题`)
  assert.match(source, /class="supported-models-refresh-button"[\s\S]*?<SyncOutlined\s*\/>/, `${sectionName} 支持模型刷新必须使用轻量同步图标`)
  assert.match(source, /\.supported-models-label\s*\{[^}]*flex:\s*1/, `${sectionName} 支持模型标签必须铺满表单标签的可用宽度`)
  assert.doesNotMatch(source, /\.supported-models-label\s*\{[^}]*width:\s*100%/, `${sectionName} 支持模型标签不得强制溢出其可用宽度`)
  assert.doesNotMatch(source, /\.supported-models-label\s*:deep\(\.ant-btn\)\s*\{[^}]*margin-right:\s*-/, `${sectionName} 刷新按钮不得使用负右边距而裁切`)
}

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
assert.equal(form.privilege, 'normal', '新建账户默认不启用任何特权')
assert.equal(form.status, 'pending_test', '新建账户默认应进入待检查状态')
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
assert.equal(savePayload.status, 'pending_test', '默认保存请求必须保持待检查状态')
assert.equal(draftPayload.healthCheckModel, 'gpt-5.4', '新增和编辑人工测试草稿必须携带表单检查模型')
assert.deepEqual(savePayload.supportedModels, ['gpt-5.5', 'gpt-5.4'], '账户保存必须保留支持模型')
form.status = 'active'
assert.equal(buildAccountSavePayload({
  accounts: [],
  form,
  errorPolicyRules: [],
  responseInspectionRules: []
}).status, 'active', '用户选择可调度时保存请求必须保留显式状态')

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
