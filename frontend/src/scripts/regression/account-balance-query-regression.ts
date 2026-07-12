import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  buildAccountBalancePayload,
  formatAccountBalance,
  validateAccountBalanceForm
} from '@/views/accounts/accountBalanceQuery'
import { replaceAccountBalanceSnapshot } from '@/views/accounts/accountListMutations'
import type { AccountSummary } from '@/types/domain'

assert.deepEqual(formatAccountBalance({ status: 'fresh', remainingUsd: '7.310000' }), {
  text: '$7.31', tone: 'fresh', tooltip: undefined, refreshing: false
})
assert.deepEqual(formatAccountBalance({ status: 'failed', remainingUsd: '7.31', errorMessage: '上游超时' }), {
  text: '查询失败', tone: 'failed', tooltip: '上游超时', refreshing: false
})
assert.equal(formatAccountBalance({ status: 'unlimited' }).text, '无限')
assert.equal(formatAccountBalance({ status: 'unsupported' }).text, '未提供')
assert.equal(formatAccountBalance({ status: 'refreshing' }).refreshing, true)

const originalAccounts = [
  { id: 'account-a', name: '账户 A', balanceSnapshot: { status: 'fresh', remainingUsd: '1.00' } },
  { id: 'account-b', name: '账户 B', balanceSnapshot: { status: 'fresh', remainingUsd: '2.00' } }
] as AccountSummary[]
const nextSnapshot = { status: 'fresh', remainingUsd: '7.31' } as const
const updatedAccounts = replaceAccountBalanceSnapshot(originalAccounts, 'account-b', nextSnapshot)
assert.notEqual(updatedAccounts, originalAccounts, '刷新余额必须替换 shallowRef 数组才能触发视图更新')
assert.equal(updatedAccounts[0], originalAccounts[0], '未刷新的账户行不应创建新对象')
assert.notEqual(updatedAccounts[1], originalAccounts[1], '刷新的账户行必须替换为新对象')
assert.equal(updatedAccounts[1]?.balanceSnapshot, nextSnapshot)
assert.equal(
  replaceAccountBalanceSnapshot(originalAccounts, 'missing-account', nextSnapshot),
  originalAccounts,
  '目标账户不在当前页时不应产生无意义更新'
)

const apiKeyForm = {
  type: 'api_key', apiKeys: ['sk-one'], balanceQueryEnabled: true,
  balanceQueryAdapter: 'builtin', balanceQueryIntervalMinutes: 5,
  balanceQueryCustomPath: '', balanceQueryRemainingPointer: '', balanceQueryTotalPointer: '',
  balanceQueryUsedPointer: '', balanceQueryDivisor: ''
} as const
assert.deepEqual(buildAccountBalancePayload(apiKeyForm), {
  balanceQueryEnabled: true,
  balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
})
assert.equal(buildAccountBalancePayload({ ...apiKeyForm, type: 'oauth' }), undefined)
assert.equal(buildAccountBalancePayload({ ...apiKeyForm, apiKeys: ['sk-one', 'sk-two'] }), undefined)
assert.equal(validateAccountBalanceForm(apiKeyForm), undefined)
assert.match(validateAccountBalanceForm({
  ...apiKeyForm,
  balanceQueryAdapter: 'custom',
  balanceQueryCustomPath: 'https://evil.example/balance',
  balanceQueryRemainingPointer: '/balance'
}) ?? '', /相对路径/)

const usageCellSource = readFileSync('../frontend/src/views/accounts/AccountUsageCell.vue', 'utf8')
const editSectionSource = readFileSync('../frontend/src/views/accounts/AccountBalanceQuerySection.vue', 'utf8')
const balanceHelperSource = readFileSync('../frontend/src/views/accounts/accountBalanceQuery.ts', 'utf8')
const accountsViewSource = readFileSync('../frontend/src/views/accounts/AccountsView.vue', 'utf8')
const accountsApiSource = readFileSync('../frontend/src/api/domains/accounts.ts', 'utf8')
assert.match(usageCellSource, /ReloadOutlined/, '余额刷新必须使用裸刷新图标')
assert.match(balanceHelperSource, /查询失败/, '失败状态必须统一显示查询失败')
assert.match(usageCellSource, /balanceDisplay\.tooltip/, '失败原因必须通过 tooltip 展示')
assert.match(editSectionSource, /balance-query-header/, '余额查询开关应放在标题行右侧')
assert.match(editSectionSource, /QuestionCircleOutlined/, '余额查询应提供帮助说明')
assert.match(editSectionSource, /测试查询/, '余额配置应提供无副作用测试入口')
assert.match(editSectionSource, /仅验证当前配置，不会保存/, '余额测试必须明确不会保存表单或快照')
assert.doesNotMatch(editSectionSource, /保存并查询|queryResult|querySnapshot|a-alert/, '余额测试不能保存或在表单内保留查询结果')
assert.match(editSectionSource, /内置适配/, '余额配置应只向用户提供内置适配模式')
assert.doesNotMatch(editSectionSource, /Sub2API|New API|LiteLLM|user_balance/, '前端不能暴露内置适配器实现细节')
assert.match(accountsViewSource, /buildAccountBalancePayload\(form\)/, '编辑页测试必须构建当前余额配置')
assert.match(accountsViewSource, /currentDraftTestPayload\(\)/, '编辑页测试必须使用当前未保存账户草稿')
assert.match(accountsViewSource, /testBalanceDraft\(/, '编辑页测试必须调用无副作用草稿接口')
assert.doesNotMatch(accountsViewSource, /balanceQueryResult/, '编辑页不能保留余额测试结果状态')
assert.match(accountsApiSource, /testBalanceDraft:/, '账户 API 必须提供无副作用草稿余额测试')
assert.match(accountsApiSource, /refreshBalance:\s*\(id: string,\s*params\?:/, '正式余额刷新不能再接收未保存配置')
const listRefreshSource = /async function refreshAccountBalance[\s\S]*?\n}/.exec(accountsViewSource)?.[0] ?? ''
assert.match(listRefreshSource, /updateLoadedAccountBalance\(accountId, snapshot\)/, '列表刷新应通过 shallowRef 列表入口替换当前账户行')
assert.doesNotMatch(listRefreshSource, /account\.balanceSnapshot = snapshot/, '列表刷新不能直接修改 shallowRef 内部对象')
assert.doesNotMatch(listRefreshSource, /refreshData\(/, '列表余额刷新不能重新请求整张账户列表')
assert.match(usageCellSource, /balance-label/, '列表余额标签和金额应分层着色')
assert.match(usageCellSource, /balance-value/, '列表余额金额应独立着色')
assert.match(usageCellSource, /font-size:\s*11px/, '列表余额刷新图标应弱化为小号图标')
assert.doesNotMatch(editSectionSource, /oneapi_compatible/i, '前端不能保留 oneapi_compatible')

console.log('account balance query frontend regression passed')
