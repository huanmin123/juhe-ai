import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const accountListSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
const accountsViewSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountsView.vue', import.meta.url)), 'utf8')

assert.match(accountListSource, /onLoaded: \(result\) =>/, '账户列表必须在分页结果写入后再触发状态快照')
assert.match(accountListSource, /loadCurrentPageStatusSnapshot\(result\.items,/, '状态快照必须使用已应用的当前页行')
assert.match(accountListSource, /api\.accounts\.statusSnapshot\(/, '管理账户列表必须使用管理侧状态快照接口')
assert.match(accountListSource, /api\.myAccounts\.statusSnapshot\(/, '个人账户列表必须使用个人侧状态快照接口')
assert.match(accountListSource, /requestId !== statusSnapshotRequestId/, '状态快照必须拒绝过期响应覆盖新页')
assert.match(accountListSource, /accountStatusSnapshotLoading/, '状态快照必须暴露独立加载状态')
assert.match(accountListSource, /accountStatusSnapshotError/, '状态快照必须暴露独立错误状态')
assert.match(accountListSource, /retryCurrentPageStatusSnapshot/, '状态快照失败必须支持当前页重试')
assert.match(accountListSource, /catch \(error\)[\s\S]*accountStatusSnapshotError\.value = '账户状态更新失败'/, '状态快照失败必须写入独立错误状态')
assert.match(accountListSource, /function retryCurrentPageStatusSnapshot\(\)[\s\S]*lastStatusSnapshotItems\.value/, '重试必须复用最近一次已应用页面的账户集合')
assert.match(accountsViewSource, /accountStatusSnapshotLoading \|\| accountStatusSnapshotError/, '账户页必须显示独立快照加载或错误状态')
assert.match(accountsViewSource, /@click="retryCurrentPageStatusSnapshot"/, '账户页错误提示必须提供重试入口')
assert.doesNotMatch(accountListSource, /dynamicSnapshot/, '账户列表不得保留动态快照补齐状态')
assert.doesNotMatch(accountListSource, /loadDynamicSnapshotsInBatches/, '账户列表不得保留分批快照请求')
assert.match(accountListSource, /const items = accountList\.items\.map\(\(account\) => accountListViewModel\(account, accountList\.runtimeSnapshot\)\)/, '账户列表必须先返回静态列表投影')

console.log('账户列表渐进动态字段回归通过：静态首包不等待，当前页状态快照异步补齐且拒绝旧响应')
