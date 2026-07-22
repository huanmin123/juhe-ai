import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const accountListSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')

assert.doesNotMatch(accountListSource, /statusSnapshot\(/, '账户列表不得请求状态快照接口')
assert.doesNotMatch(accountListSource, /dynamicSnapshot/, '账户列表不得保留动态快照补齐状态')
assert.doesNotMatch(accountListSource, /loadDynamicSnapshotsInBatches/, '账户列表不得保留分批快照请求')
assert.match(accountListSource, /items: accountList\.items\.map\(\(account\) => accountListViewModel\(account, accountList\.runtimeSnapshot\)\)/, '账户列表必须直接消费列表响应内的动态字段')

console.log('账户列表内联动态字段回归通过：无额外状态快照请求')
