import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const editFormSource = readSource('src/views/accounts/useAccountEditForm.ts')
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
