import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  accountBalanceWillAutoDisable,
  buildAccountBalancePayload,
  canManuallyRefreshAccountBalance,
  formatAccountBalance,
  validateAccountBalanceForm
} from '@/views/accounts/accountBalanceQuery'
import { replaceAccountBalanceSnapshot } from '@/views/accounts/accountListMutations'
import type { AccountSummary } from '@/types/domain'

assert.deepEqual(formatAccountBalance({ status: 'fresh', remainingUsd: '7.310000' }), {
  text: '$7.31', tone: 'fresh', tooltip: undefined, refreshing: false, visible: true
})
assert.deepEqual(formatAccountBalance({ status: 'fresh', remainingUsd: '0.000000' }), {
  text: '$0.00', tone: 'fresh', tooltip: undefined, refreshing: false, visible: true
}, '成功的零余额必须展示为 $0.00，而不是查询失败')
assert.deepEqual(formatAccountBalance({ status: 'fresh', remainingUsd: '-0.250037' }), {
  text: '-$0.25', tone: 'fresh', tooltip: undefined, refreshing: false, visible: true
}, '成功的透支余额必须展示实际负值，而不是查询失败')
assert.deepEqual(formatAccountBalance({ status: 'failed', remainingUsd: '7.31', errorMessage: '上游超时' }), {
  text: '余额查询失败', tone: 'failed', tooltip: '上游超时', refreshing: false, visible: true
})
assert.deepEqual(formatAccountBalance({ status: 'unlimited' }), {
  text: '不限额', tone: 'unlimited', tooltip: undefined, refreshing: false, visible: true
})
assert.deepEqual(formatAccountBalance({ status: 'unsupported', errorMessage: '当前配置未找到可用余额接口' }), {
  text: '余额查询失败', tone: 'failed', tooltip: '当前配置未找到可用余额接口', refreshing: false, visible: true
})
assert.deepEqual(formatAccountBalance(undefined), {
  text: '待查询', tone: 'pending', tooltip: undefined, refreshing: false, visible: true
})
assert.deepEqual(formatAccountBalance({ status: 'pending' }), {
  text: '待查询', tone: 'pending', tooltip: undefined, refreshing: false, visible: true
})
assert.deepEqual(formatAccountBalance({ status: 'refreshing' }), {
  text: '查询中', tone: 'refreshing', tooltip: undefined, refreshing: true, visible: true
})
assert.equal(canManuallyRefreshAccountBalance({ balanceQueryEnabled: true, status: 'disabled', accessType: 'owner' }), true, '停用的自有账户仍可人工刷新余额')
assert.equal(canManuallyRefreshAccountBalance({ balanceQueryEnabled: true, status: 'error', accessType: 'owner' }), true, '异常的自有账户仍可人工刷新余额')
assert.equal(canManuallyRefreshAccountBalance({ balanceQueryEnabled: true, status: 'active', accessType: 'authorized' }), false, '授权实例不能越权刷新来源账户余额')
assert.deepEqual(formatAccountBalance({ status: 'refreshing', remainingUsd: '3.210000' } as never), {
  text: '$3.21', tone: 'fresh', tooltip: undefined, refreshing: true, visible: true
})
assert.deepEqual(formatAccountBalance({
  status: 'fresh',
  remainingUsd: '7.310000',
  consecutiveTransientFailures: 1,
  lastTransientErrorMessage: '上游余额查询超时'
} as never), {
  text: '$7.31',
  tone: 'fresh',
  tooltip: '刷新暂时失败（1/3）：上游余额查询超时；当前显示上次成功余额',
  refreshing: false,
  visible: true
})
assert.deepEqual(formatAccountBalance({
  status: 'pending',
  consecutiveTransientFailures: 2,
  lastTransientErrorMessage: '上游暂时不可用'
} as never), {
  text: '暂时无法查询',
  tone: 'pending',
  tooltip: '上游暂时不可用',
  refreshing: false,
  visible: true
})

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
assert.deepEqual(buildAccountBalancePayload({ ...apiKeyForm, apiKeys: ['sk-one', 'sk-two'] }), {
  balanceQueryEnabled: false,
  balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
})
assert.equal(accountBalanceWillAutoDisable({ ...apiKeyForm, apiKeys: ['sk-one', 'sk-two'] }), true)
assert.equal(accountBalanceWillAutoDisable({ ...apiKeyForm, apiKeys: ['sk-one', ' sk-one '] }), false, '重复 Key 不应误判为多 Key')
assert.equal(validateAccountBalanceForm({ ...apiKeyForm, apiKeys: ['sk-one', 'sk-two'] }), undefined, '多 Key 自动关闭余额查询，不能阻断保存')
assert.deepEqual(buildAccountBalancePayload({
  ...apiKeyForm,
  apiKeys: ['sk-one', 'sk-two'],
  balanceQueryAdapter: 'custom',
  balanceQueryCustomPath: ''
}), {
  balanceQueryEnabled: false,
  balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
}, '多 Key 隐藏余额配置后不能因未完成的自定义草稿阻断保存')
assert.equal(validateAccountBalanceForm(apiKeyForm), undefined)
assert.match(validateAccountBalanceForm({
  ...apiKeyForm,
  balanceQueryAdapter: 'custom',
  balanceQueryCustomPath: 'https://evil.example/balance',
  balanceQueryRemainingPointer: '/balance'
}) ?? '', /相对路径/)

const usageCellSource = readFileSync('../frontend/src/views/accounts/AccountUsageCell.vue', 'utf8')
const editSectionSource = readFileSync('../frontend/src/views/accounts/AccountBalanceQuerySection.vue', 'utf8')
const apiKeySectionSource = readFileSync('../frontend/src/views/accounts/AccountApiKeySection.vue', 'utf8')
const saveFlowSource = readFileSync('../frontend/src/views/accounts/useAccountEditSaveFlow.ts', 'utf8')
const savePayloadSource = readFileSync('../frontend/src/views/accounts/accountSavePayload.ts', 'utf8')
const balanceHelperSource = readFileSync('../frontend/src/views/accounts/accountBalanceQuery.ts', 'utf8')
const accountsViewSource = readFileSync('../frontend/src/views/accounts/AccountsView.vue', 'utf8')
const accountsApiSource = readFileSync('../frontend/src/api/domains/accounts.ts', 'utf8')
assert.match(usageCellSource, /ReloadOutlined/, '余额刷新必须使用裸刷新图标')
assert.match(balanceHelperSource, /查询失败/, '失败状态必须统一显示查询失败')
assert.match(usageCellSource, /balanceDisplay\.tooltip/, '失败原因必须通过 tooltip 展示')
assert.match(usageCellSource, /v-if="account\.balanceQueryEnabled" class="balance-row"/, '余额开启后必须始终保留人工刷新入口')
assert.match(usageCellSource, /<a-tooltip v-if="balanceDisplay\.visible"[^>]*>[\s\S]*?<span class="balance-text"/, '只有余额文本按快照可见性控制')
assert.match(balanceHelperSource, /text: tone === 'refreshing' \? '查询中' : '待查询'/, '无余额快照时必须展示明确状态，不能只留下刷新图标')
assert.doesNotMatch(usageCellSource, /props\.account\.status === 'active'/, '人工刷新不得依赖账户状态')
assert.match(usageCellSource, /v-if="balanceDisplay\.tone !== 'failed'" class="balance-label"/, '余额查询失败不能带“剩余：”前缀')
assert.match(editSectionSource, /balance-query-header/, '余额查询开关应放在标题行右侧')
assert.match(editSectionSource, /QuestionCircleOutlined/, '余额查询应提供帮助说明')
assert.match(apiKeySectionSource, /多 Key 账户不支持余额查询，保存后将自动关闭余额查询/, 'API Key 区域必须明确提示多 Key 自动关闭余额查询')
assert.match(savePayloadSource, /buildAccountBalancePayload\(input\.form\)/, '统一账户保存 payload 必须明确提交多 Key 余额关闭状态')
assert.match(saveFlowSource, /已因多 Key 自动关闭余额查询/, '保存成功后应明确提示自动关闭结果')
assert.match(editSectionSource, /测试查询/, '余额配置应提供无副作用测试入口')
assert.match(editSectionSource, /仅验证当前配置，不会保存/, '余额测试必须明确不会保存表单或快照')
assert.match(editSectionSource, /class="balance-query-refresh-control"[\s\S]*?<a-input-number[\s\S]*?<a-button/, '测试查询按钮必须放在刷新周期控件右侧')
assert.doesNotMatch(editSectionSource, /class="balance-query-test"/, '测试查询不能再单独占据一行')
assert.match(editSectionSource, /\.balance-query-refresh-control\s*\{[\s\S]*grid-template-columns:\s*minmax\(0,\s*1fr\)\s+auto/, '刷新周期控件必须为输入框和按钮保留稳定列宽')
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
assert.ok(
  listRefreshSource.indexOf('updateLoadedAccountBalance(accountId, snapshot)') < listRefreshSource.indexOf("snapshot?.status === 'failed' || snapshot?.status === 'unsupported'"),
  '人工刷新失败或不支持时也必须先更新当前行，不能继续展示旧金额'
)
assert.doesNotMatch(listRefreshSource, /account\.balanceSnapshot = snapshot/, '列表刷新不能直接修改 shallowRef 内部对象')
assert.doesNotMatch(listRefreshSource, /refreshData\(/, '列表余额刷新不能重新请求整张账户列表')
assert.match(usageCellSource, /balance-label/, '列表余额标签和金额应分层着色')
assert.match(usageCellSource, /balance-value/, '列表余额金额应独立着色')
assert.match(usageCellSource, /font-size:\s*11px/, '列表余额刷新图标应弱化为小号图标')
assert.doesNotMatch(editSectionSource, /oneapi_compatible/i, '前端不能保留 oneapi_compatible')

console.log('account balance query frontend regression passed')
