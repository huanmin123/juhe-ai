import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  buildAccountBalancePayload,
  formatAccountBalance,
  validateAccountBalanceForm
} from '@/views/accounts/accountBalanceQuery'

assert.deepEqual(formatAccountBalance({ status: 'fresh', remainingUsd: '7.310000' }), {
  text: '$7.31', tone: 'fresh', tooltip: undefined, refreshing: false
})
assert.deepEqual(formatAccountBalance({ status: 'failed', remainingUsd: '7.31', errorMessage: '上游超时' }), {
  text: '查询失败', tone: 'failed', tooltip: '上游超时', refreshing: false
})
assert.equal(formatAccountBalance({ status: 'unlimited' }).text, '无限')
assert.equal(formatAccountBalance({ status: 'unsupported' }).text, '未提供')
assert.equal(formatAccountBalance({ status: 'refreshing' }).refreshing, true)

const apiKeyForm = {
  type: 'api_key', apiKeys: ['sk-one'], balanceQueryEnabled: true,
  balanceQueryAdapter: 'newapi', balanceQueryIntervalMinutes: 5,
  balanceQueryCustomPath: '', balanceQueryRemainingPointer: '', balanceQueryTotalPointer: '',
  balanceQueryUsedPointer: '', balanceQueryDivisor: ''
} as const
assert.deepEqual(buildAccountBalancePayload(apiKeyForm), {
  balanceQueryEnabled: true,
  balanceQueryConfig: { adapter: 'newapi', intervalMinutes: 5 }
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
assert.match(usageCellSource, /ReloadOutlined/, '余额刷新必须使用裸刷新图标')
assert.match(balanceHelperSource, /查询失败/, '失败状态必须统一显示查询失败')
assert.match(usageCellSource, /balanceDisplay\.tooltip/, '失败原因必须通过 tooltip 展示')
assert.match(editSectionSource, /balance-query-header/, '余额查询开关应放在标题行右侧')
assert.match(editSectionSource, /QuestionCircleOutlined/, '余额查询应提供帮助说明')
assert.match(editSectionSource, /立即查询/, '余额配置应提供手动查询入口')
assert.match(editSectionSource, /queryResult/, '余额配置应展示手动查询结果')
assert.match(usageCellSource, /balance-label/, '列表余额标签和金额应分层着色')
assert.match(usageCellSource, /balance-value/, '列表余额金额应独立着色')
assert.match(usageCellSource, /font-size:\s*11px/, '列表余额刷新图标应弱化为小号图标')
assert.doesNotMatch(editSectionSource, /oneapi_compatible/i, '前端不能保留 oneapi_compatible')

console.log('account balance query frontend regression passed')
