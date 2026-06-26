import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { buildAccountDraftTestPayload } from '../../src/views/accounts/accountDraftTestPayload'
import { accountCreatePayloadWithActivationTest } from '../../src/views/accounts/accountEditFormPayload'
import type { AccountFormModel } from '../../src/views/accounts/accountFormTypes'
import { buildAccountSavePayload, validateAccountSaveForm } from '../../src/views/accounts/accountSavePayload'
import { FALLBACK_PROVIDERS } from '../../src/views/accounts/accountOptions'
import {
  defaultAccountModelMappingUpstreamEndpointFamily,
  isAccountModelMappingProtocolAllowed,
  isAccountModelMappingSourceEndpointFamilyAllowed
} from '../../src/views/accounts/accountModelMappingProtocolMatrix'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const apiKeySectionSource = readSource('src/views/accounts/AccountApiKeySection.vue')
const editModalSource = readSource('src/views/accounts/AccountEditModal.vue')
const credentialsSource = readSource('src/views/accounts/accountCredentials.ts')
const editFormSource = readSource('src/views/accounts/useAccountEditForm.ts')
const strategySectionSource = readSource('src/views/accounts/AccountStrategySection.vue')
const testModalSource = readSource('src/views/accounts/useAccountTestModal.ts')
const savePayloadSource = readSource('src/views/accounts/accountSavePayload.ts')
const saveFlowSource = readSource('src/views/accounts/useAccountEditSaveFlow.ts')
const packageJson = JSON.parse(readSource('package.json')) as { scripts?: Record<string, string> }

assertIncludes(
  editFormSource,
  "import { useAccountEditSaveFlow } from './useAccountEditSaveFlow'",
  '账户编辑表单应通过保存流程 composable 承接保存编排'
)
assertIncludes(editFormSource, '} = useAccountEditSaveFlow({', '账户编辑表单应调用 useAccountEditSaveFlow')
assertIncludes(editFormSource, 'function openCreate()', '账户编辑表单应继续保留新建弹窗生命周期')
assertIncludes(editFormSource, 'async function openEdit(', '账户编辑表单应继续保留编辑弹窗生命周期')
assertIncludes(editFormSource, 'async function openClone(', '账户编辑表单应继续保留克隆弹窗生命周期')
assertIncludes(editFormSource, 'async function loadAccountDetailForForm(', '账户编辑表单应继续负责详情装载请求')
assertIncludes(editFormSource, 'function editingAccountScopeParams()', '账户编辑表单应继续提供编辑账号 scope 桥接')
assertIncludes(editFormSource, 'function accountCreatePayloadWithActivationTest(', '账户编辑表单应继续桥接草稿测试激活来源')
assertIncludes(editFormSource, 'function openAuthUrl()', '账户编辑表单应继续只负责打开已生成授权链接')

assertNotIncludes(apiKeySectionSource, 'v-if="editing"', 'API Key 编辑态不应降级为单输入框')
assertIncludes(apiKeySectionSource, 'const showApiKeyStrategy = computed(() => filledApiKeyCount.value > 1)', 'API Key 策略切换应按有效 Key 数量展示')
assertIncludes(credentialsSource, 'api_keys: apiKeys.length ? apiKeys : undefined', 'API Key 保存 payload 应显式携带当前 Key 数组，支持多 Key 编辑成单 Key')
assertIncludes(savePayloadSource, "if (form.type === 'api_key' && apiKeyCount === 0) return '请填写 API Key'", 'API Key 编辑和创建都必须至少保留一个 Key')
assertNotIncludes(savePayloadSource, "!editingId && form.type === 'api_key' && apiKeyCount", 'API Key 数量校验不应只限制创建态')
assertIncludes(editModalSource, 'const confirmButtonProps = computed(() => ({', '账户弹窗应统一计算确定按钮状态')
assertIncludes(editModalSource, 'disabled: Boolean(props.okButtonProps.disabled) || props.testLoading', '账户测试运行期间不应允许保存成未激活账户')
assertIncludes(editModalSource, 'v-bind="confirmButtonProps"', '账户弹窗确定按钮应使用测试运行保护后的按钮属性')
assertIncludes(testModalSource, 'syncDraftActivationTestFromTask(latestTask, activationDraftPayload)', '草稿测试任务轮询拿到成功结果时应立即记录可激活任务')
assertIncludes(testModalSource, "successfulDraftActivationTest.value = { taskId: task.id, account: activationDraftPayload }", '成功草稿测试应绑定测试任务与创建表单快照')
assertIncludes(savePayloadSource, 'supported_endpoint_modes?: AccountFormModel', 'OAuth 创建 payload 应允许透传接口能力限制')
assertIncludes(savePayloadSource, 'credentialsPatch.supported_endpoint_modes', 'OAuth 创建 common payload 应把接口能力写入 credentialsPatch')
assertIncludes(savePayloadSource, 'accountModelMappingProtocolValidationMessage', '前端保存校验必须复用模型映射协议矩阵 helper')
assertIncludes(strategySectionSource, 'isAccountModelMappingProtocolAllowed', '模型映射 UI 右侧协议选择必须复用协议矩阵 helper')
assertIncludes(strategySectionSource, 'isAccountModelMappingSourceEndpointFamilyAllowed', '模型映射 UI 左侧协议选择必须复用协议矩阵 helper')
assertApiKeyDraftActivationPayload()
assertModelMappingProtocolValidation()
assertModelMappingProtocolMatrixHelper()

for (const marker of [
  "submitAction('accounts.save'",
  'function saveAuthorizedAccountEdit',
  'function createOAuthAccountFromUnifiedForm',
  'function createApiKeyAccount',
  'useSubmitAction',
  'OpenAIAuthURLResult',
  'buildAccountSavePayload',
  'buildAccountUpdatePayload',
  'buildOAuthCreatePayload',
  'buildOAuthCreateCommonPayload',
  'validateAccountSaveForm',
  'normalizeFormTagNames',
  'sameTagNames'
]) {
  assertNotIncludes(editFormSource, marker, `账户编辑表单不应继续内联保存流程片段：${marker}`)
}

for (const marker of [
  "useSubmitAction('accounts')",
  "submitAction('accounts.save'",
  'const saving = submittingRef',
  'const authLoading = ref(false)',
  'const authResult = ref<OpenAIAuthURLResult>()',
  'validateAccountSaveForm',
  'buildAccountSavePayload',
  'buildAccountUpdatePayload',
  'async function saveAuthorizedAccountEdit',
  'buildOAuthCreateCommonPayload',
  'buildOAuthCreatePayload',
  'async function createOAuthAccountFromUnifiedForm',
  'async function createApiKeyAccount',
  'createOAuthAccountFromUnifiedForm(options.accountCreatePayloadWithActivationTest(payload))',
  "created?.status === 'active' ? 'OAuth 账户已创建并启用'",
  "options.form.oauthMode === 'refresh_token' && activationPayload.activationTestTaskId",
  'commonPayload.activationTestTaskId = activationPayload.activationTestTaskId',
  'api.accounts.update',
  'api.myAccounts.update',
  'api.accounts.create',
  'api.myAccounts.create',
  'api.accounts.bindGroup',
  'api.myAccounts.bindGroup',
  'api.accounts.updateAuthorizedDispatch',
  'api.myAccounts.updateAuthorizedDispatch',
  'api.accounts.updateTags',
  'api.myAccounts.updateTags',
  'api.openaiOAuth.authUrl',
  'api.myOpenaiOAuth.authUrl',
  'api.openaiOAuth.createFromCode',
  'api.myOpenaiOAuth.createFromCode',
  'api.openaiOAuth.createFromRefreshToken',
  'api.myOpenaiOAuth.createFromRefreshToken',
  'options.accountCreatePayloadWithActivationTest',
  'options.clearSuccessfulDraftActivationTest()',
  'options.modalOpen.value = false',
  'await options.loadData()',
  'async function generateOAuthUrl'
]) {
  assertIncludes(saveFlowSource, marker, `账户编辑保存流程应承接保存/OAuth/API 分流片段：${marker}`)
}

for (const marker of [
  'function openCreate',
  'function openEdit',
  'function openClone',
  'function loadAccountDetailForForm',
  'window.open('
]) {
  assertNotIncludes(saveFlowSource, marker, `保存流程 composable 不应承接弹窗生命周期或浏览器打开动作：${marker}`)
}

assert.equal(
  packageJson.scripts?.['test:account-edit-save-flow'],
  'pnpm --dir ../backend exec tsx --tsconfig ../frontend/tsconfig.json ../frontend/scripts/regression/account-edit-save-flow-regression.ts',
  '前端 package script 应暴露账户编辑保存流程边界回归'
)

console.log('账户编辑保存流程边界回归通过：保存/OAuth/API 分流已从主表单 composable 拆出')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}

function assertIncludes(source: string, marker: string, message: string): void {
  assert(source.includes(marker), message)
}

function assertNotIncludes(source: string, marker: string, message: string): void {
  assert.equal(source.includes(marker), false, message)
}

function assertApiKeyDraftActivationPayload(): void {
  const form: AccountFormModel = {
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    name: 'API Key 草稿激活账户',
    type: 'api_key',
    groupId: 'grp_api_key_activation',
    group: { id: 'grp_api_key_activation', name: 'API Key 激活分组' },
    apiKey: '',
    apiKeys: ['sk-regression-a', 'sk-regression-b'],
    apiKeyStrategy: 'weighted_round_robin',
    apiKeyWeights: [2, 5],
    baseUrl: 'https://api.openai.com/v1',
    accessToken: '',
    refreshToken: '',
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    concurrencyLimit: 20,
    priority: 0,
    clientCompatibility: 'codex_responses',
    supportedEndpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
    supportedModels: ['gpt-5.5'],
    modelMappings: [{
      sourceModel: 'gpt-5.5-alias',
      sourceEndpointFamily: 'chat_completions',
      upstreamModel: 'gpt-5.5-chat-latest',
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }],
    tags: ['回归'],
    proxyProfileId: undefined,
    availabilitySchedule: { enabled: false, mode: 'weekly', timezone: 'Asia/Shanghai', weekly: [] },
    notes: 'API Key 草稿测试成功后保存应直接启用'
  }
  const input = {
    accounts: [],
    editingId: undefined,
    form,
    errorPolicyRules: [],
    responseInspectionRules: []
  }
  const savePayload = buildAccountSavePayload(input)
  const draftPayload = buildAccountDraftTestPayload(input)
  assert.equal(savePayload.clientCompatibility, 'codex_responses', '账号保存 payload 必须显式提交账号级客户端兼容字段')
  assert.equal(draftPayload.clientCompatibility, 'codex_responses', '草稿测试账号 payload 必须显式提交账号级客户端兼容字段')
  assert.deepEqual(savePayload.modelMappings, [{
    sourceModel: 'gpt-5.5-alias',
    sourceEndpointFamily: 'chat_completions',
    upstreamModel: 'gpt-5.5-chat-latest',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }], '账号保存 payload 应保留 Chat Completions 同协议模型别名')
  assert.deepEqual(draftPayload.modelMappings, [{
    sourceModel: 'gpt-5.5-alias',
    sourceEndpointFamily: 'chat_completions',
    upstreamModel: 'gpt-5.5-chat-latest',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }], '草稿测试 payload 应保留 Chat Completions 同协议模型别名')
  const activatedPayload = accountCreatePayloadWithActivationTest(savePayload, {
    taskId: 'accttest_api_key_activation',
    account: draftPayload
  }, form.name.trim())

  assert.equal(activatedPayload.status, 'active', 'API Key 新增账户成功草稿测试后保存 payload 应创建为正常状态')
  assert.equal(activatedPayload.activationTestTaskId, 'accttest_api_key_activation', 'API Key 新增账户成功草稿测试后保存 payload 应携带测试任务')
}

function assertModelMappingProtocolValidation(): void {
  const baseForm = apiKeyFormFixture()
  assert.equal(validateForm(baseForm), undefined, 'OpenAI 协议账号应允许 Chat Completions 同协议模型别名')
  assert.equal(validateForm({
    ...baseForm,
    supportedEndpointModes: ['responses_json', 'responses_sse'],
    modelMappings: [{
      sourceModel: 'gpt-5.5',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'gpt-5.5-responses-native',
      upstreamEndpointFamily: 'responses',
      enabled: true
    }]
  }), undefined, '前端保存前应允许真实 Responses 上游的 Responses -> Responses 直连映射')
  assert.match(validateForm({
    ...baseForm,
    supportedEndpointModes: ['responses_json', 'responses_sse'],
    modelMappings: [{
      sourceModel: 'gpt-5.5-chat',
      sourceEndpointFamily: 'chat_completions',
      upstreamModel: 'gpt-5.5-responses-native',
      upstreamEndpointFamily: 'responses',
      enabled: true
    }]
  }) ?? '', /账号模型别名只支持同协议映射/, '前端保存前应拒绝 Chat Completions -> Responses')
  assert.match(validateForm({
    ...baseForm,
    supportedEndpointModes: ['chat_json', 'chat_sse'],
    modelMappings: [{
      sourceModel: 'gpt-5.5',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'gpt-5.5-chat-latest',
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }]
  }) ?? '', /账号模型别名只支持同协议映射/, '前端保存前应拒绝 Chat-only 上游承接 Responses -> Chat Completions')
  assert.match(validateForm({
    ...baseForm,
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    supportedEndpointModes: ['messages_json', 'messages_sse'],
    supportedModels: ['claude-sonnet-4-6'],
    modelMappings: [{
      sourceModel: 'gpt-5.5',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'claude-sonnet-4-6',
      upstreamEndpointFamily: 'messages',
      enabled: true
    }]
  }) ?? '', /账号模型别名只支持同协议映射/, '前端保存前应拒绝 OpenAI Responses -> Anthropic Messages 显式映射')
  assert.match(validateForm({
    ...baseForm,
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    supportedEndpointModes: ['messages_json', 'messages_sse'],
    supportedModels: ['claude-haiku-4-5'],
    modelMappings: [
      {
        sourceModel: 'gemini-3.5-flash',
        sourceEndpointFamily: 'generate_content',
        upstreamModel: 'claude-haiku-4-5',
        upstreamEndpointFamily: 'messages',
        enabled: true
      },
      {
        sourceModel: 'gemini-3.5-flash',
        sourceEndpointFamily: 'stream_generate_content',
        upstreamModel: 'claude-haiku-4-5',
        upstreamEndpointFamily: 'messages',
        enabled: true
      }
    ]
  }) ?? '', /账号模型别名不支持 Gemini GenerateContent 跨协议映射/, '前端保存前应拒绝 Gemini GenerateContent / StreamGenerateContent -> Anthropic Messages 显式映射')
  assert.match(validateForm({
    ...baseForm,
    modelMappings: [{
      sourceModel: 'claude-sonnet-4-6',
      sourceEndpointFamily: 'messages',
      upstreamModel: 'gpt-5.5-chat-latest',
      upstreamEndpointFamily: 'responses',
      enabled: true
    }]
  }) ?? '', /账号模型别名不支持 Anthropic Messages 跨协议映射/, '前端保存前应拒绝 Messages -> Responses')
  assert.match(validateForm({
    ...baseForm,
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    supportedEndpointModes: ['messages_json', 'messages_sse'],
    supportedModels: ['claude-haiku-4-5'],
    modelMappings: [{
      sourceModel: 'gemini-3.5-flash',
      sourceEndpointFamily: 'generate_content',
      upstreamModel: 'claude-haiku-4-5',
      upstreamEndpointFamily: 'responses',
      enabled: true
    }]
  }) ?? '', /账号模型别名不支持 Gemini GenerateContent 跨协议映射/, '前端保存前应拒绝 Gemini GenerateContent -> Responses')
  assert.match(validateForm({
    ...baseForm,
    modelMappings: [{
      sourceModel: 'gemini-3.5-flash',
      sourceEndpointFamily: 'generate_content',
      upstreamModel: 'claude-haiku-4-5',
      upstreamEndpointFamily: 'messages',
      enabled: true
    }]
  }) ?? '', /账号模型别名不支持 Gemini GenerateContent 跨协议映射/, '前端保存前应拒绝 OpenAI 档案配置 Gemini GenerateContent -> Anthropic Messages')
  assert.match(validateForm({
    ...baseForm,
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    supportedEndpointModes: ['messages_json', 'messages_sse'],
    modelMappings: [{
      sourceModel: 'gpt-5.5',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'claude-sonnet-4-6',
      upstreamEndpointFamily: 'responses',
      enabled: true
    }]
  }) ?? '', /当前供应商协议不支持 OpenAI 账号模型别名|Anthropic 协议账号模型别名只能使用 Messages/, '前端保存前应拒绝 Anthropic 档案配置 OpenAI 上游协议')
  assert.match(validateForm({
    ...baseForm,
    providerCode: 'deepseek',
    providerProtocolProfileId: 'profile_deepseek_openai_v1',
    clientCompatibility: 'openai_standard',
    supportedEndpointModes: ['chat_json', 'chat_sse'],
    modelMappings: [{
      sourceModel: 'gpt-5.5',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'gpt-5.5-chat-latest',
      upstreamEndpointFamily: 'responses',
      enabled: true
    }]
  }) ?? '', /Responses 模型别名只能用于账号真实支持 Responses API/, '前端保存前应拒绝无原生 Responses endpoint mode 的右侧 Responses')
  assert.match(validateForm({
    ...baseForm,
    providerCode: 'gemini',
    providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta',
    supportedEndpointModes: ['chat_json', 'chat_sse'],
    modelMappings: [{
      sourceModel: 'claude-sonnet-4-6',
      sourceEndpointFamily: 'messages',
      upstreamModel: 'gemini-2.5-pro',
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }]
  }) ?? '', /Gemini OpenAI Chat 账号模型别名只能使用 Chat Completions|账号模型别名不支持 Anthropic Messages 跨协议映射/, '前端保存前应拒绝 Gemini OpenAI Chat 的 Messages 来源映射')
}

function assertModelMappingProtocolMatrixHelper(): void {
  const gptProfile = protocolProfile('gpt', 'profile_gpt_openai_v1')
  const anthropicProfile = protocolProfile('anthropic', 'profile_anthropic_anthropic_v1')
  const geminiOpenAIChatProfile = protocolProfile('gemini', 'profile_gemini_openai_chat_v1beta')
  const geminiNativeProfile = protocolProfile('gemini', 'profile_gemini_native_v1beta')

  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'chat_completions',
    upstreamEndpointFamily: 'chat_completions',
    context: { providerProfile: gptProfile, supportedEndpointModes: ['chat_json', 'chat_sse'] }
  }), true, '矩阵 helper 应允许 Chat Completions 同协议模型别名')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'chat_completions',
    context: { providerProfile: gptProfile, supportedEndpointModes: ['chat_json', 'chat_sse'] }
  }), false, '矩阵 helper 应拒绝 Responses -> Chat Completions')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'chat_completions',
    upstreamEndpointFamily: 'responses',
    context: { providerProfile: gptProfile, supportedEndpointModes: ['chat_json', 'chat_sse'] }
  }), false, '矩阵 helper 应拒绝无原生 Responses 能力的 Chat -> Responses')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'chat_completions',
    upstreamEndpointFamily: 'responses',
    context: { providerProfile: gptProfile, supportedEndpointModes: ['responses_json'] }
  }), false, '矩阵 helper 应拒绝真实 Responses 能力下的 Chat -> Responses')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'responses',
    context: { providerProfile: gptProfile, supportedEndpointModes: ['responses_json'] }
  }), true, '矩阵 helper 应允许 Responses 同协议模型别名')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'messages',
    context: { providerProfile: anthropicProfile, supportedEndpointModes: ['messages_json', 'messages_sse'] }
  }), false, '矩阵 helper 应拒绝 OpenAI Responses -> Anthropic Messages')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'responses',
    context: { providerProfile: gptProfile, supportedEndpointModes: ['responses_json'] }
  }), false, '矩阵 helper 应拒绝 Anthropic Messages -> Responses')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'messages',
    upstreamEndpointFamily: 'messages',
    context: { providerProfile: anthropicProfile, supportedEndpointModes: ['messages_json'] }
  }), true, '矩阵 helper 应允许 Anthropic Messages 同协议模型别名')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'generate_content',
    upstreamEndpointFamily: 'messages',
    context: { providerProfile: anthropicProfile, supportedEndpointModes: ['messages_json'] }
  }), false, '矩阵 helper 应拒绝 Gemini GenerateContent -> Anthropic Messages')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'stream_generate_content',
    upstreamEndpointFamily: 'generate_content',
    context: { providerProfile: geminiNativeProfile, supportedEndpointModes: ['generate_content_json'] }
  }), true, '矩阵 helper 应允许 Gemini StreamGenerateContent 到 GenerateContent 别名')
  assert.equal(isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily: 'stream_generate_content',
    upstreamEndpointFamily: 'responses',
    context: { providerProfile: gptProfile, supportedEndpointModes: ['responses_sse'] }
  }), false, '矩阵 helper 应拒绝 Gemini StreamGenerateContent -> Responses')
  assert.equal(isAccountModelMappingSourceEndpointFamilyAllowed('messages', {
    providerProfile: geminiOpenAIChatProfile,
    supportedEndpointModes: ['chat_json', 'chat_sse']
  }), false, 'Gemini OpenAI Chat UI 不应允许选择 Messages 下游来源')
  assert.equal(defaultAccountModelMappingUpstreamEndpointFamily('generate_content', {
    providerProfile: geminiNativeProfile,
    supportedEndpointModes: ['generate_content_json']
  }), 'generate_content', 'Gemini native 档案下 Gemini 来源默认右侧协议应是 GenerateContent')
}

function apiKeyFormFixture(): AccountFormModel {
  return {
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    name: '协议矩阵校验账户',
    type: 'api_key',
    groupId: 'grp_protocol_matrix',
    group: { id: 'grp_protocol_matrix', name: '协议矩阵分组' },
    apiKey: '',
    apiKeys: ['sk-regression-protocol-matrix'],
    apiKeyStrategy: 'round_robin',
    apiKeyWeights: [],
    baseUrl: 'https://api.openai.com/v1',
    accessToken: '',
    refreshToken: '',
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    concurrencyLimit: 20,
    priority: 0,
    clientCompatibility: 'codex_responses',
    supportedEndpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
    supportedModels: ['gpt-5.5'],
    modelMappings: [{
      sourceModel: 'gpt-5.5-alias',
      sourceEndpointFamily: 'chat_completions',
      upstreamModel: 'gpt-5.5-chat-latest',
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }],
    tags: [],
    proxyProfileId: undefined,
    availabilitySchedule: { enabled: false, mode: 'weekly', timezone: 'Asia/Shanghai', weekly: [] },
    notes: ''
  }
}

function validateForm(form: AccountFormModel): string | undefined {
  return validateAccountSaveForm({
    form,
    hasAuthSession: false,
    errorPolicyRules: [],
    responseInspectionRules: [],
    providers: FALLBACK_PROVIDERS
  })
}

function protocolProfile(providerCode: string, profileId: string) {
  const provider = FALLBACK_PROVIDERS.find((item) => item.code === providerCode)
  const profile = provider?.protocolProfiles.find((item) => item.id === profileId)
  assert(profile, `缺少测试协议档案：${providerCode} / ${profileId}`)
  return profile
}
