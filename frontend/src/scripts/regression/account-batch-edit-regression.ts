import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  buildAccountBatchEditRequest,
  createAccountBatchEditForm
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
assert.match(modalSource, /batchEditContext\(/, '批量编辑应在打开弹窗后一次性按需读取去敏上下文')
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

function accountFixture(id: string, configRevision: number): AccountSummary {
  return {
    id,
    configRevision,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: id,
    type: 'api_key',
    credentials: { supported_endpoint_modes: ['chat_sse'] },
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
