import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const accountListSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
const accountsViewSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountsView.vue', import.meta.url)), 'utf8')

assert.doesNotMatch(accountListSource, /statusSnapshot|loadCurrentPageStatusSnapshot|statusSnapshotRequestId/, '账户列表加载不得补发状态快照请求')
assert.doesNotMatch(accountListSource, /mergeAccountStatusSnapshot/, '账户列表不得在首包后合并动态快照')
assert.doesNotMatch(accountsViewSource, /<a-alert/, '账户页不得显示状态快照横幅')
assert.doesNotMatch(accountListSource, /dynamicSnapshot/, '账户列表不得保留动态快照补齐状态')
assert.doesNotMatch(accountListSource, /loadDynamicSnapshotsInBatches/, '账户列表不得保留分批快照请求')
assert.match(accountListSource, /items: accountList\.items as AccountSummary\[\]/, '账户列表必须直接消费完整列表条目')

console.log('账户列表内联动态字段回归通过：列表响应直接包含当前状态，页面不再补发状态快照请求')
