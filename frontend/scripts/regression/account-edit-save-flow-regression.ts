import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { buildAccountDraftTestPayload } from '../../src/views/accounts/accountDraftTestPayload'
import { defaultAccountForm } from '../../src/views/accounts/accountFormDefaults'
import { FALLBACK_PROVIDERS } from '../../src/views/accounts/accountOptions'
import { systemInheritedErrorPolicyRulesPreview } from '../../src/views/accounts/accountErrorPolicyTypes'
import {
  buildAccountSavePayload,
  validateAccountSaveForm
} from '../../src/views/accounts/accountSavePayload'
import {
  buildAccountAdvancedUpdatePatch,
  buildAccountBasicEditSnapshot,
  buildAccountBasicUpdatePatch
} from '../../src/views/accounts/accountEditPatch'
import {
  createSavedAccountApiKeyRuntimeSnapshot,
  visibleSavedAccountApiKeyRuntimeDetails
} from '../../src/views/accounts/accountApiKeyRuntimeDisplay'
import { openAIOAuthClientPayload } from '../../src/views/accounts/accountOAuthPayload'
import { accountListParams } from '../../src/api/params'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const accountsViewSource = readSource('src/views/accounts/AccountsView.vue')
const editModalSource = readSource('src/views/accounts/AccountEditModal.vue')
const errorPolicyCardSource = readSource('src/views/accounts/AccountErrorPolicyCard.vue')
const errorPolicyLoaderSource = readSource('src/views/accounts/accountEditFormLoaders.ts')
const basicInfoSource = readSource('src/views/accounts/AccountBasicInfoSection.vue')
const editTestSource = readSource('src/views/accounts/useAccountEditTestAction.ts')
const saveFlowSource = readSource('src/views/accounts/useAccountEditSaveFlow.ts')
const editFormSource = readSource('src/views/accounts/useAccountEditForm.ts')
const editGroupOptionsSource = readSource('src/views/accounts/useAccountEditGroupOptions.ts')
const groupOptionsSource = readSource('src/views/accounts/useAccountGroupOptions.ts')
const tagOptionsSource = readSource('src/views/accounts/useAccountEditTagOptions.ts')
const providerModelOptionsSource = readSource('src/views/accounts/useAccountProviderModelOptions.ts')
const savePayloadSource = readSource('src/views/accounts/accountSavePayload.ts')
const draftTestPayloadSource = readSource('src/views/accounts/accountDraftTestPayload.ts')
const testModalSource = readSource('src/views/accounts/useAccountTestModal.ts')
const apiKeySectionSource = readSource('src/views/accounts/AccountApiKeySection.vue')
const healthCheckModelFieldSource = readSource('src/views/accounts/AccountHealthCheckModelField.vue')
const tagSelectSource = readSource('src/views/accounts/AccountTagSelect.vue')
const oauthSectionSource = readSource('src/views/accounts/AccountOAuthSection.vue')
const reauthorizeSource = readSource('src/views/accounts/useAccountReauthorize.ts')
const reauthorizeModalSource = readSource('src/views/accounts/AccountReauthorizeModal.vue')
const accountApiSource = readSource('src/api/domains/accounts.ts')
const accountMenuActionsSource = readSource('src/views/accounts/useAccountMenuActions.ts')
const batchEditModalSource = readSource('src/views/accounts/AccountBatchEditModal.vue')
const oauthDomainSources = [
  ['OpenAI', readSource('src/api/domains/openaiOAuth.ts')],
  ['Anthropic', readSource('src/api/domains/anthropicOAuth.ts')],
  ['Gemini', readSource('src/api/domains/geminiOAuth.ts')],
  ['Grok', readSource('src/api/domains/grokOAuth.ts')]
] as const
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

const createOpenSource = sourceBetween(editFormSource, 'async function openCreate()', 'watch(')
const providerSelectSource = sourceBetween(editFormSource, 'function selectProvider(', 'function selectAccountType(')
const typeSelectSource = sourceBetween(editFormSource, 'function selectAccountTypeChoice(', 'function applyCachedDefaultGroup(')
const cachedDefaultGroupSource = sourceBetween(editFormSource, 'function applyCachedDefaultGroup(', 'async function openEdit(')
const editOpenSource = sourceBetween(editFormSource, 'async function openEdit(', 'async function ensureAccountEditDetailLoaded(')
const cloneOpenSource = sourceBetween(editFormSource, 'async function openClone(', 'async function loadAccountDetailForForm(')
for (const [flow, source] of [
  ['新增弹窗', createOpenSource],
  ['切换供应商', providerSelectSource],
  ['切换账户类型', typeSelectSource],
  ['编辑弹窗', editOpenSource],
  ['克隆弹窗', cloneOpenSource]
] as const) {
  assert.doesNotMatch(
    source,
    /loadAccountOptions\(|loadGroupOptions\(|loadAccountTagOptions\(|loadCurrentProviderModelOptions\(|loadProviderModelOptions\(|fetchAccountApiKeyRuntimeForEdit\(|\.apiKeyRuntime\(/,
    `${flow}不得预取供应商、分组、标签、模型或 API Key 运行态`
  )
}
assert.match(editOpenSource, /'edit-basic'/, '普通编辑弹窗首开只允许请求 edit-basic')
assert.match(basicInfoSource, /<a-form-item class="dispatch-status-field" label="状态">/, '新增和编辑必须展示同一组三态状态选择')
assert.match(basicInfoSource, /@change="form\.statusSelectionExplicit = true"/, '手动切换状态必须标记为显式选择')
assert.doesNotMatch(healthCheckModelFieldSource, /allow-clear/, '必填检查模型不得暴露清空入口并生成后端不接受的 null PATCH')
assert.match(savePayloadSource, /if \(!healthCheckModel\) return '请选择检查模型'/, '保存前必须在前端拦截空检查模型')
assert.match(editFormSource, /supportedModels\[0\] \?\? ''/, '已选检查模型离开支持模型集合时应回落到首个可用模型')
assert.match(cachedDefaultGroupSource, /getCachedUserReferenceData\(referenceParams\)/, '新增表单应直接读取登录后预热的默认分组缓存')
assert.doesNotMatch(cachedDefaultGroupSource, /loadUserReferenceData|api\./, '默认分组缓存缺失时弹窗不得补发网络请求')
assert.match(cachedDefaultGroupSource, /if \(defaultGroup\) \{[\s\S]*setFormGroup\(defaultGroup\)[\s\S]*return[\s\S]*ensureDefaultGroupSelected\(providerCode\)/, '缓存命中时必须直接复用默认分组，缺失时只能从已加载本地选项选择')
assert.match(editFormSource, /accounts: ReadonlyValue<AccountListItem\[\]>/, '编辑表单只能把账户列表当作展示 DTO 使用')
assert.match(errorPolicyLoaderSource, /loadInheritedErrorPolicyRules\(advanced\.effectiveErrorHandlingRules\)/, '错误策略表单加载必须从高级 DTO 提取继承规则')
assert.match(errorPolicyLoaderSource, /filter\(\(rule\) => rule\.source === 'system' && rule\.inherited === true && rule\.editable === false\)/, '只有不可编辑系统规则可进入继承展示列表')
assert.match(errorPolicyLoaderSource, /inheritedErrorPolicyRules: systemInheritedErrorPolicyRulesPreview\(\)/, '克隆账户在尚无高级详情 DTO 时也必须展示只读系统规则')
assert.match(editFormSource, /inheritedErrorPolicyRules\.value = systemInheritedErrorPolicyRulesPreview\(\)/, '新建账户在尚无高级详情 DTO 时也必须展示只读系统规则')
assert.match(editModalSource, /:inherited-error-policy-rules="inheritedErrorPolicyRules"/, '错误策略卡片必须接收独立继承规则列表')
assert.match(errorPolicyCardSource, /<a-tag v-if="item\.inherited" color="cyan">继承<\/a-tag>/, '继承规则必须显示继承标签')
assert.match(errorPolicyCardSource, /!readonly && !item\.inherited/, '继承规则不得显示编辑、排序或删除操作')
assert.match(errorPolicyCardSource, /displayRules = computed\(\(\) => \[/, '系统规则与本地规则必须合并在同一展示列表')
assert.match(errorPolicyCardSource, /inheritedErrorPolicyRules\.map[\s\S]*rules\.value\.map/, '展示列表必须系统规则优先且本地规则仍来自可保存表单')
assert.match(
  editFormSource,
  /editingAccountDetail = ref<AccountEditBasicDetail>\(\)[\s\S]*editingAccountAdvancedDetail = ref<AccountAdvancedDetail>\(\)/,
  '编辑详情必须使用独立的 edit-basic 基础详情与高级详情状态'
)
assert.match(
  editFormSource,
  /level: 'edit-basic'[\s\S]*?Promise<AccountEditBasicDetail \| undefined>/,
  'edit-basic 请求必须返回专用基础详情 DTO'
)
assert.match(editFormSource, /function handleAccountTagOptionsDropdown\(open: boolean\)[\s\S]*?if \(open\) void loadAccountTagOptions/, '标签候选只能在展开下拉后加载')
assert.match(
  groupOptionsSource,
  /watch\(\s*currentCatalogScopeKey[\s\S]*?resetOptions\(\)[\s\S]*?flush: 'sync'/,
  '分组候选必须在 provider 或 owner scope 切换的同步时刻失效'
)
assert.match(
  groupOptionsSource,
  /function isCurrentRequest\([\s\S]*?requestCatalogScopeKey === currentCatalogScopeKey\(\)/,
  '分组候选响应写回前必须复核当前 provider 与 owner scope'
)
assert.match(groupOptionsSource, /function handleDropdown\(open: boolean\)[\s\S]*?clearSearchTimer\(\)/, '关闭分组下拉必须清理搜索定时器')
assert.match(groupOptionsSource, /onBeforeUnmount\(resetOptions\)/, '卸载分组选项组合函数时必须失效请求并清理定时器')
assert.match(editGroupOptionsSource, /function resetEditGroupOptions\(\)[\s\S]*?groupOptions\.reset\(\)/, '账户编辑关闭时必须可清空分组候选')
assert.match(accountsViewSource, /watch\(modalOpen[\s\S]*?clearAccountModelOptionsSearchTimer\(\)[\s\S]*?resetEditGroupOptions\(\)/, '账户编辑关闭时必须清理模型搜索和分组选项状态')
assert.match(
  tagOptionsSource,
  /watch\(\s*currentAccountTagOptionsScopeKey[\s\S]*?resetAccountTagOptions\(\)[\s\S]*?flush: 'sync'/,
  '标签候选必须在 owner scope 切换的同步时刻失效'
)
assert.match(tagOptionsSource, /scopeKey === currentAccountTagOptionsScopeKey\(\)/, '标签候选和删除响应写回前必须复核当前 owner scope')
assert.match(
  providerModelOptionsSource,
  /watch\(\s*currentProviderModelCatalogScopeKey[\s\S]*?resetProviderModelOptions\(\)[\s\S]*?flush: 'sync'/,
  '供应商模型候选必须在 provider 或 owner scope 切换的同步时刻失效'
)
assert.match(
  providerModelOptionsSource,
  /requestCatalogScopeKey === currentProviderModelCatalogScopeKey\(\)/,
  '供应商模型响应写回前必须复核当前 provider 与 owner scope'
)
assert.match(
  editFormSource,
  /function resetDeferredAccountOptionState\(\)[\s\S]*?resetProviderModelOptions\(\)[\s\S]*?resetAllProviderModelOptions\(\)[\s\S]*?resetAccountTagOptions\(\)/,
  '关闭或切换账户表单必须清空所有模型候选和标签候选'
)
assert.match(tagSelectSource, /@dropdown-visible-change="\$emit\('dropdown-visible-change', \$event\)"/, '标签选择器必须透传下拉展开事件')
assert.match(apiKeySectionSource, /@dropdown-visible-change="\$emit\('model-options-open', \$event\)"/, 'API Key 支持模型下拉必须发出按需加载事件')
assert.match(editModalSource, /<AccountApiKeySection[\s\S]*?@model-options-open="\$emit\('model-options-open', \$event\)"[\s\S]*?@model-options-search="\$emit\('model-options-search', \$event\)"/, '账户弹窗必须把 API Key 模型展开和搜索事件传到页面加载器')
assert.match(editFormSource, /async function loadAccountApiKeyRuntimeDetails\(force = false\)[\s\S]*?apiKeys\.length < 2[\s\S]*?fetchAccountApiKeyRuntimeForEdit/, 'API Key 运行态只能由显式多 Key 加载动作请求')
assert.match(saveFlowSource, /if \(options\.editingId\.value && !options\.accountAdvancedDetailLoaded\.value\)[\s\S]*?saveBasicAccountEdit\(\)/, '未加载高级配置时必须走基础字段增量保存')
assert.doesNotMatch(
  `${savePayloadSource}\n${draftTestPayloadSource}`,
  /currentAccountCredentials|accounts\.find\([^)]*\)\?\.credentials/,
  '保存和草稿测试不得从列表反查凭据'
)
assert.match(
  savePayloadSource,
  /currentCredentials: input\.accountDetail\?\.credentials/,
  '保存载荷只能使用点击后加载的编辑详情凭据'
)
assert.match(
  saveFlowSource,
  /function requiredAccountConfigRevision\([\s\S]*?账户配置版本缺失或无效/,
  '普通和高级编辑必须在 PATCH 前明确校验配置修订号'
)
assert.match(
  saveFlowSource,
  /const payload = buildAccountBasicUpdatePatch\([\s\S]*?if \(!payload\) \{[\s\S]*?return[\s\S]*?api\.accounts\.update/,
  '基础编辑无变化时必须在 PATCH 前直接返回'
)
assert.match(saveFlowSource, /await refreshEditedAccountRows\(updated\)/, '编辑成功必须定点刷新后端确认的账户行')
assert.match(
  accountsViewSource,
  /markAccountMutation\(mutation\)[\s\S]*mutation\.authorizationInstancesAffected[\s\S]*await reloadAccountPageAfterMutation\(\)[\s\S]*return/,
  '来源账户编辑必须先推进列表代次，再重建服务端分页窗口以覆盖未加载的授权实例'
)
assert.match(
  accountsViewSource,
  /const ids = \[mutation\.id\][\s\S]*api\.accounts\.list\([\s\S]*api\.myAccounts\.list\([\s\S]*accountUpdateAffectsPageWindow\(account\)[\s\S]*await reloadAccountPageAfterMutation\(\)/,
  '普通变更应先定点刷新；影响筛选或排序时必须等待服务端分页窗口刷新'
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
assert.doesNotMatch(
  accountsViewSource,
  /shouldAutoRefreshAccountModelCatalog|scheduleAutomaticAccountModelCatalogSync|automaticModelCatalogAttemptedRequestKeys|clearAccountModelCatalogAutoSyncTimer/,
  '账户弹窗不得包含已移除的自动模型目录同步状态或调度器'
)
const manualModelCatalogRefreshSource = sourceBetween(
  accountsViewSource,
  'async function refreshAccountModelCatalog(): Promise<void> {',
  'let accountModelOptionsSearchTimer'
)
assert.match(accountsViewSource, /@refresh-models="refreshAccountModelCatalog"/, '账户弹窗必须保留用户手动同步上游模型入口')
assert.equal(
  [...manualModelCatalogRefreshSource.matchAll(/api\.(?:accounts|myAccounts)\.refreshModelCatalog/g)].length,
  2,
  '上游模型目录同步只能保留在手动同步处理函数中'
)
assert.doesNotMatch(
  accountsViewSource.replace(manualModelCatalogRefreshSource, ''),
  /\brefreshAccountModelCatalog\s*\(/,
  '新增、编辑、取消和其他弹窗生命周期不得调用上游模型目录同步'
)
assert.doesNotMatch(
  accountsViewSource.replace(manualModelCatalogRefreshSource, ''),
  /api\.(?:accounts|myAccounts)\.refreshModelCatalog/,
  '上游模型目录接口不得在手动同步处理函数外调用'
)
assert.doesNotMatch(
  editFormSource,
  /refreshAccountModelCatalog|refreshModelCatalog/,
  '账户表单打开、切换、取消和卸载生命周期不得调用上游模型目录同步'
)
assert.doesNotMatch(
  `${editTestSource}\n${saveFlowSource}`,
  /refreshAccountModelCatalog|refreshModelCatalog/,
  '测试和确认只能校验模型，不得自动同步上游模型目录'
)
assert.match(
  editTestSource,
  /const validationMessage = validateAccountDraftTestForm\([\s\S]*?if \(validationMessage\) \{[\s\S]*?message\.warning\(validationMessage\)[\s\S]*?return/,
  '测试必须在模型校验失败后中止，不得继续创建草稿测试'
)
assert.match(
  saveFlowSource,
  /const validationMessage = validateAccountSaveForm\([\s\S]*?if \(validationMessage\) \{[\s\S]*?message\.warning\(validationMessage\)[\s\S]*?return/,
  '新增和完整编辑确认必须在模型校验失败后中止，不得继续保存账户'
)
const basicEditSaveSource = sourceBetween(saveFlowSource, 'async function saveBasicAccountEdit(): Promise<void> {', 'function finishUnchangedEdit(): void {')
assert.match(
  basicEditSaveSource,
  /if \(!healthCheckModel\) \{[\s\S]*?message\.warning\('请选择检查模型'\)[\s\S]*?return/,
  '基础编辑确认必须在检查模型为空时中止，不得继续保存账户'
)
assert.match(
  accountsViewSource,
  /requestId === modelCatalogSyncRequestId && !controller\.signal\.aborted\)[\s\S]*?message\.error/,
  '模型目录同步失败必须保留可观察错误提示'
)
assert.match(
  accountsViewSource,
  /if \(requestId !== modelCatalogSyncRequestId \|\| requestKey !== currentModelCatalogDiscoveryRequestKey\(\)\) return/,
  '目录同步响应返回时必须确认请求仍对应当前草稿'
)
assert.match(
  accountsViewSource,
  /loadUserReferenceData\(\{ viewScope: 'admin', systemAccountId \}\)\.catch\(\(\) => undefined\)/,
  '管理视图选定目标用户后应异步预热其默认分组与路由引用'
)
assert.match(
  reauthorizeSource,
  /async function openReauthorizeModal[\s\S]*providerKind === 'gemini'[\s\S]*oauthReauthorizationContext[\s\S]*reauthorizeModalOpen\.value = true/,
  '重新授权只能在 Gemini 用户点击后读取窄 OAuth 元数据上下文，其他供应商直接使用列表版本'
)
assert.doesNotMatch(reauthorizeSource, /editBasicDetail/, '重新授权不得读取包含凭据和模型关系的 edit-basic')
assert.doesNotMatch(
  reauthorizeSource,
  /account\.credentials/,
  '重新授权不得从列表行读取凭据'
)
assert.match(
  editFormSource,
  /async function openClone[\s\S]*cloneContext\(account\.id[\s\S]*buildAccountCloneFormLoad/,
  '克隆只能通过单次 clone-context 请求加载同版本的窄表单上下文'
)
const cloneOpenImplementation = editFormSource.match(/async function openClone[\s\S]*?async function loadAccountDetailForForm/)?.[0] ?? ''
assert.doesNotMatch(
  cloneOpenImplementation,
  /editBasicDetail|advancedDetail|Promise\.all/,
  '克隆不得再拼接编辑详情响应或并发加载额外详情'
)
assert.match(
  cloneOpenImplementation,
  /const sourceGroupId = sourceAccount\.boundGroupId[\s\S]*?\?\? options\.groupIdForAccount\(account\.id\)[\s\S]*?\?\? account\.boundGroupId/,
  '克隆必须优先使用 clone-context 的分组，仅在该窄响应缺失时回退已加载列表分组'
)
assert.match(cloneOpenSource, /buildAccountCloneFormLoad\(\{[\s\S]*account: sourceAccount/, '克隆表单只能消费专用上下文')
assert.doesNotMatch(cloneOpenSource, /sourceAccount\.credentials|credentials:/, '克隆表单不得读取或清空建号秘密凭据容器')
assert.match(accountApiSource, /cloneContext:[\s\S]*\/clone-context/, '账户 API 必须提供专用 clone-context 契约')
assert.match(accountApiSource, /oauthReauthorizationContext:[\s\S]*\/oauth-reauthorization-context/, '账户 API 必须提供专用 OAuth 重授权上下文契约')
assert.match(oauthSectionSource, /isOpenAI[\s\S]*form\.oauthMode === 'refresh_token'[\s\S]*form\.googleClientId/, 'OpenAI Refresh Token 建号必须提供可选 Client ID 输入')
assert.match(reauthorizeModalSource, /providerKind === 'openai' && form\.oauthMode === 'refresh_token'[\s\S]*form\.googleClientId/, 'OpenAI Refresh Token 重授权必须提供可选 Client ID 输入')
assert.match(saveFlowSource, /openAIOAuthClientPayload\(form\)[\s\S]*openaiOAuth\.createFromRefreshToken\(openAIPayload/, 'OpenAI Refresh Token 建号必须单独附加 clientId，不能污染其他供应商 payload')
assert.match(reauthorizeSource, /providerKind === 'openai'[\s\S]*openAIOAuthClientPayload\(reauthorizeForm\)/, 'OpenAI Refresh Token 重授权必须附加 clientId')

for (const [providerName, source] of oauthDomainSources) {
  assert.doesNotMatch(source, /unwrap<AccountSummary>/, `${providerName} OAuth 凭据轮换接口不得再接收完整账户响应`)
  assert.match(
    source,
    /refreshToken: \(id: string, payload: OAuthCredentialRotationPayload[\s\S]*unwrap<OAuthCredentialRotationResult>/,
    `${providerName} 主动刷新令牌必须携带配置版本并接收最小轮换回执`
  )
  assert.equal(
    [...source.matchAll(/reauthorizeFrom(?:Code|RefreshToken):[\s\S]*?unwrap<OAuthCredentialRotationResult>/g)].length,
    4,
    `${providerName} 管理端和用户端重新授权接口都必须接收最小轮换回执`
  )
}

const saveReauthorizeSource = sourceBetween(reauthorizeSource, 'async function saveReauthorize()', '\n  return {')
const reauthorizeSuccessSource = sourceBetween(saveReauthorizeSource, 'options.updateLoadedAccountRevision', '} catch (error) {')
assert.match(saveReauthorizeSource, /expectedConfigRevision/, '重新授权请求必须携带当前账户配置版本')
assert.match(saveReauthorizeSource, /updateLoadedAccountRevision\(account\.id, updated\.configRevision\)/, '重新授权成功后必须只推进列表行配置版本')
assert.doesNotMatch(reauthorizeSuccessSource, /options\.loadData\(/, '重新授权成功后不得刷新完整账户列表')
assert.match(saveReauthorizeSource, /isOAuthConfigRevisionConflict\(error\)[\s\S]*options\.loadData\(\)/, '重新授权只有版本冲突时才刷新账户列表')

const refreshOAuthTokenSource = sourceBetween(accountMenuActionsSource, 'async function refreshOAuthToken(', 'async function updateAccountState(')
const refreshOAuthSuccessSource = sourceBetween(refreshOAuthTokenSource, 'options.updateLoadedAccountRevision', '} catch (error) {')
assert.match(refreshOAuthTokenSource, /payload = \{ expectedConfigRevision \}/, '主动刷新令牌请求必须携带当前账户配置版本')
assert.match(refreshOAuthTokenSource, /updateLoadedAccountRevision\(account\.id, updated\.configRevision\)/, '主动刷新令牌成功后必须只推进列表行配置版本')
assert.doesNotMatch(refreshOAuthSuccessSource, /options\.loadData\(/, '主动刷新令牌成功后不得刷新完整账户列表')
assert.match(refreshOAuthTokenSource, /isOAuthConfigRevisionConflict\(error\)[\s\S]*options\.loadData\(\)/, '主动刷新令牌只有版本冲突时才刷新账户列表')

const openAIOAuthForm = defaultAccountForm('gpt', 'oauth', FALLBACK_PROVIDERS)
assert.deepEqual(openAIOAuthClientPayload(openAIOAuthForm), {}, 'OpenAI Client ID 留空时必须由后端使用内置默认值')
openAIOAuthForm.googleClientId = '  app_custom_mobile  '
assert.deepEqual(openAIOAuthClientPayload(openAIOAuthForm), { clientId: 'app_custom_mobile' }, 'OpenAI 自定义 Client ID 必须去除首尾空白后进入 payload')
const accountPageMountedSource = sourceBetween(accountsViewSource, 'onMounted(() => {', '})\n</script>')
assert.doesNotMatch(
  accountPageMountedSource,
  /loadFilterAccountTagOptions|loadGroupOptions|loadCurrentProviderModelOptions/,
  '账户页挂载不得预取标签、分组或模型候选'
)
const batchEditContextSource = sourceBetween(batchEditModalSource, 'async function loadContext', 'async function loadModelOptions')
assert.doesNotMatch(
  batchEditContextSource,
  /loadModelOptions/,
  '打开批量编辑弹窗不得预取模型目录'
)
assert.match(
  batchEditModalSource,
  /@dropdown-visible-change="handleMappingModelOptionsOpen"/,
  '批量编辑模型候选只能在用户展开下拉后加载'
)
assert.match(
  accountsViewSource,
  /if \(!form\.healthCheckModel\.trim\(\) && result\.recommendedHealthCheckModel\) form\.healthCheckModel = result\.recommendedHealthCheckModel/,
  '同步上游目录只可在检查模型为空时采用推荐值，不能覆盖用户手动选择'
)
assert.match(
  accountsViewSource,
  /onBeforeUnmount\(\(\) => \{[\s\S]*?clearAccountModelOptionsSearchTimer\(\)[\s\S]*?cancelAccountModelCatalogSync\(\)/,
  '离开账户页面时必须清理模型候选延迟任务与上游模型同步请求'
)
for (const [sectionName, source] of [['API Key', apiKeySectionSource], ['OAuth', oauthSectionSource]] as const) {
  assert.doesNotMatch(source, /<a-form-item required tooltip=/, `${sectionName} 支持模型说明不得由表单标签尾部自动渲染`)
  assert.match(source, /<span>支持模型<\/span>\s*<a-tooltip[^>]*>\s*<QuestionCircleOutlined class="supported-models-help"/s, `${sectionName} 支持模型说明图标必须紧跟标题`)
  const supportedModelsLabelSource = sourceBetween(source, '<div class="supported-models-label">', '</div>')
  const supportedModelsControlSource = sourceBetween(source, '<div class="supported-models-control">', '</div>')
  assert.doesNotMatch(supportedModelsLabelSource, /supported-models-refresh-button|refresh-models|SyncOutlined/, `${sectionName} 支持模型标签不得包含刷新按钮或刷新事件`)
  assert.match(supportedModelsControlSource, /<a-select[\s\S]*<a-button[\s\S]*class="supported-models-refresh-button"/, `${sectionName} 刷新按钮必须位于支持模型选择控件之后`)
  assert.match(supportedModelsControlSource, /aria-label="从上游同步可新增模型"/, `${sectionName} 刷新按钮必须提供中文无障碍名称`)
  assert.match(supportedModelsControlSource, /@click\.stop="\$emit\('refresh-models'\)"/, `${sectionName} 只有同步图标按钮可触发刷新事件`)
  assert.match(source, /class="supported-models-refresh-button"[\s\S]*?<SyncOutlined\s*\/>/, `${sectionName} 支持模型刷新必须使用轻量同步图标`)
  assert.match(source, /\.supported-models-label\s*\{[^}]*flex:\s*1/, `${sectionName} 支持模型标签必须铺满表单标签的可用宽度`)
  assert.doesNotMatch(source, /\.supported-models-label\s*\{[^}]*width:\s*100%/, `${sectionName} 支持模型标签不得强制溢出其可用宽度`)
  assert.doesNotMatch(source, /\.supported-models-label\s*:deep\(\.ant-btn\)\s*\{[^}]*margin-right:\s*-/, `${sectionName} 刷新按钮不得使用负右边距而裁切`)
  assert.match(source, /\.supported-models-control\s*\{[^}]*display:\s*flex[\s\S]*?min-width:\s*0/, `${sectionName} 支持模型控件必须使用弹性布局并允许收缩`)
  assert.match(source, /\.supported-models-control\s*:deep\(\.ant-select\)\s*\{[^}]*flex:\s*1[\s\S]*?min-width:\s*0/, `${sectionName} 支持模型选择器必须占据剩余宽度且允许收缩`)
  assert.match(source, /\.supported-models-refresh-button\s*\{[^}]*width:\s*24px[\s\S]*?height:\s*24px/, `${sectionName} 支持模型刷新按钮必须保持 24px 尺寸`)
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
assert.deepEqual(
  visibleSavedAccountApiKeyRuntimeDetails(savedRuntimeSnapshot, ['sk-saved-b', 'sk-saved-a'])?.map((item) => ({
    keyIndex: item.keyIndex,
    keyFingerprintPrefix: item.keyFingerprintPrefix
  })),
  [
    { keyIndex: 0, keyFingerprintPrefix: 'fingerprint-b' },
    { keyIndex: 1, keyFingerprintPrefix: 'fingerprint-a' }
  ],
  '重排 Key 后必须按 Key 身份重映射运行状态，避免按旧 keyIndex 错贴'
)
assert.equal(createSavedAccountApiKeyRuntimeSnapshot({
  accountId: 'account-runtime-display',
  configRevision: 8,
  apiKeys: ['sk-saved-a', 'sk-saved-b'],
  response: savedRuntimeResponse
}), undefined, '配置版本不一致时不得接受并行返回的旧运行状态')

const form = defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
const createInheritedRules = systemInheritedErrorPolicyRulesPreview()
assert.deepEqual(
  createInheritedRules.map((rule) => ({ id: rule.id, source: rule.source, inherited: rule.inherited, editable: rule.editable })),
  [{ id: 'system.upstream_insufficient_quota', source: 'system', inherited: true, editable: false }],
  '新建和克隆账户必须展示同一条只读系统额度规则预览'
)
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
assert.equal('error_handling_rules' in savePayload.credentials, false, '只读系统规则预览不得进入新建账户的 credentials payload')
assert.equal(draftPayload.healthCheckModel, 'gpt-5.4', '新增和编辑人工测试草稿必须携带表单检查模型')
assert.deepEqual(savePayload.supportedModels, ['gpt-5.5', 'gpt-5.4'], '账户保存必须保留支持模型')

const basicBaseline = buildAccountBasicEditSnapshot(form, {
  api_key: form.apiKey,
  base_url: form.baseUrl,
  supported_endpoint_modes: form.supportedEndpointModes,
  service_tier_override: 'priority'
})
assert.equal(
  buildAccountBasicUpdatePatch(basicBaseline, basicBaseline, 7),
  undefined,
  '基础编辑没有变化时不得产生更新请求体'
)
const renamedBasic = structuredClone(basicBaseline)
renamedBasic.values.name = '仅修改名称'
assert.deepEqual(
  buildAccountBasicUpdatePatch(renamedBasic, basicBaseline, 7),
  { name: '仅修改名称', expectedConfigRevision: 7 },
  '基础编辑单字段变化时只能提交该字段和并发修订号'
)
const changedCredential = structuredClone(basicBaseline)
changedCredential.credentials.base_url = 'https://changed.example.com/v1'
assert.deepEqual(
  buildAccountBasicUpdatePatch(changedCredential, basicBaseline, 7),
  {
    credentialsPatch: { base_url: 'https://changed.example.com/v1' },
    expectedConfigRevision: 7
  },
  '凭据变化必须使用 credentialsPatch 浅增量，不能回传完整 credentials'
)
const removedCredential = structuredClone(basicBaseline)
delete removedCredential.credentials.api_key
assert.deepEqual(
  buildAccountBasicUpdatePatch(removedCredential, basicBaseline, 7),
  {
    credentialsPatch: { api_key: null },
    expectedConfigRevision: 7
  },
  '删除基础凭据字段必须显式发送 null，不能全量覆盖凭据对象'
)
assert.throws(
  () => buildAccountBasicUpdatePatch(renamedBasic, basicBaseline, 0),
  /账户配置版本无效/,
  '基础编辑缺少合法修订号时必须在请求前失败'
)

const advancedBaseline = structuredClone(savePayload)
assert.equal(
  buildAccountAdvancedUpdatePatch(advancedBaseline, advancedBaseline, 11),
  undefined,
  '高级编辑没有变化时不得产生更新请求体'
)
const advancedPriorityChanged = structuredClone(advancedBaseline)
advancedPriorityChanged.priority += 1
assert.deepEqual(
  buildAccountAdvancedUpdatePatch(advancedPriorityChanged, advancedBaseline, 11),
  { priority: advancedPriorityChanged.priority, expectedConfigRevision: 11 },
  '高级编辑单字段变化时只能提交该字段和并发修订号'
)
const advancedHealthModelCleared = structuredClone(advancedBaseline)
delete advancedHealthModelCleared.healthCheckModel
assert.deepEqual(
  buildAccountAdvancedUpdatePatch(advancedHealthModelCleared, advancedBaseline, 11),
  { healthCheckModel: null, expectedConfigRevision: 11 },
  '清空高级字段必须发送 null，不能回传整份账户配置'
)
assert.throws(
  () => buildAccountAdvancedUpdatePatch(advancedPriorityChanged, advancedBaseline, Number.NaN),
  /账户配置版本无效/,
  '高级编辑缺少合法修订号时必须在请求前失败'
)
assert.equal('service_tier_override' in basicBaseline.credentials, false, '未加载的高级凭据字段不得进入基础编辑基线')
assert.equal('balanceQueryConfig' in basicBaseline.values, false, '未加载的高级账户字段不得进入基础编辑增量')
assert.equal('status' in basicBaseline.values, false, '普通编辑未显式选择状态时不得提交状态')
const basicStatusChanged = structuredClone(basicBaseline)
basicStatusChanged.values.status = 'active'
assert.deepEqual(
  buildAccountBasicUpdatePatch(basicStatusChanged, basicBaseline, 7),
  { status: 'active', expectedConfigRevision: 7 },
  '普通编辑显式选择状态时必须提交状态'
)
const advancedStatusChanged = structuredClone(advancedBaseline)
advancedStatusChanged.status = 'active'
assert.deepEqual(
  buildAccountAdvancedUpdatePatch(advancedStatusChanged, advancedBaseline, 11, true),
  { status: 'active', expectedConfigRevision: 11 },
  '高级编辑显式选择状态时必须提交 status'
)
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
  'pnpm --filter juhe-ai-backend exec tsx --tsconfig ../frontend/tsconfig.json ../frontend/scripts/regression/account-edit-save-flow-regression.ts',
  '前端 package script 应暴露账户编辑保存流程回归'
)
assert.deepEqual(
  accountListParams({ ids: ['account-source', 'account-authorized'], page: 1, pageSize: 2 }),
  { ids: 'account-source,account-authorized', page: 1, pageSize: 2 },
  '账户列表定点刷新必须把当前加载行 ID 序列化为窄 ids 查询'
)

console.log('账户编辑保存流程回归通过：详情分层、凭据边界、增量 PATCH、修订号和零请求 no-op 均符合契约')

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = source.indexOf(endMarker, start + startMarker.length)
  assert.notEqual(start, -1, `缺少源码起点：${startMarker}`)
  assert.notEqual(end, -1, `缺少源码终点：${endMarker}`)
  return source.slice(start, end)
}
