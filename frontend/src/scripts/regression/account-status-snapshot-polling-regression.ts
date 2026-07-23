import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const accountListSource = readFileSync(new URL('../../views/accounts/useAccountListData.ts', import.meta.url), 'utf8')
const accountMutationsSource = readFileSync(new URL('../../views/accounts/accountListMutations.ts', import.meta.url), 'utf8')

assert.doesNotMatch(
  accountListSource,
  /statusSnapshot|loadCurrentPageStatusSnapshot|statusSnapshotRequestId/,
  '账户列表只能通过列表接口和用户主动刷新获取最新状态，不得请求状态快照'
)
assert.doesNotMatch(
  accountMutationsSource,
  /mergeAccountStatusSnapshot/,
  '前端不得保留账户状态快照合并入口'
)

console.log('账户状态快照防回退通过：账户列表只在列表加载或用户主动刷新时获取最新状态')
