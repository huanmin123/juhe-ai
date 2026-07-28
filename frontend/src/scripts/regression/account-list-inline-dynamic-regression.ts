import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const accountListSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
const accountsViewSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountsView.vue', import.meta.url)), 'utf8')
const accountTypesSource = readFileSync(fileURLToPath(new URL('../../types/domain/accounts.ts', import.meta.url)), 'utf8')
const accountRulesSource = readFileSync(fileURLToPath(new URL('../../views/accounts/accountRules.ts', import.meta.url)), 'utf8')
const accountUsageCellSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountUsageCell.vue', import.meta.url)), 'utf8')

assert.doesNotMatch(accountListSource, /statusSnapshot|loadCurrentPageStatusSnapshot|statusSnapshotRequestId/, '账户列表加载不得补发状态快照请求')
assert.doesNotMatch(accountListSource, /mergeAccountStatusSnapshot/, '账户列表不得在首包后合并动态快照')
assert.doesNotMatch(accountsViewSource, /<a-alert/, '账户页不得显示状态快照横幅')
assert.doesNotMatch(accountListSource, /dynamicSnapshot/, '账户列表不得保留动态快照补齐状态')
assert.doesNotMatch(accountListSource, /loadDynamicSnapshotsInBatches/, '账户列表不得保留分批快照请求')
assert.match(accountListSource, /useResponsivePagedList<AccountListItem,/, '账户列表状态必须使用轻量 AccountListItem DTO')
assert.doesNotMatch(accountListSource, /as AccountSummary\[\]/, '账户列表不得把轻量 DTO 强转成完整账户详情')
assert.match(accountTypesSource, /export interface AccountListResult \{[\s\S]*?items: AccountListItem\[\]/, '列表响应必须显式返回 AccountListItem')
const listItemContract = accountTypesSource.match(/export interface AccountListItem[\s\S]*?\n\}\n\nexport interface AccountEditBasicDetail/)?.[0] ?? ''
assert.ok(listItemContract, '必须能定位 AccountListItem 契约')
assert.doesNotMatch(listItemContract, /\| 'credentials'|\| 'oauthUsage'|\| 'authorizationSources'|\| 'usage'/, '列表 DTO 不得暴露凭据、OAuth 用量、授权来源数组或完整用量对象')
assert.doesNotMatch(accountRulesSource, /authorizationSources/, '列表规则不得读取授权来源完整数组')
assert.doesNotMatch(accountUsageCellSource, /oauthUsage|credentials|authorizationSources/, '列表用量单元格不得读取详情字段')

console.log('账户列表轻量 DTO 回归通过：列表直接消费 AccountListItem，且不读取凭据、OAuth 用量或授权来源数组')
