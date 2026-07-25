import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const repositorySource = readFileSync(new URL('../../storage/account-runtime-mutation.repository.ts', import.meta.url), 'utf8')

const cooldownAsyncSource = functionSource('markAccountCooldownAsync', 'migrateAccountTraffic')
const exceptionAsyncSource = functionSource('markAccountExceptionAsync', 'markAccountDisabledByFailure')
const authorizedCooldownAsyncSource = functionSource('markAuthorizedAccountBindingCooldownByContextAsync', 'markAuthorizedAccountBindingDisabledByFailure')
const ownerClearAsyncSource = sourceBetween(
  'export async function clearAccountFailureStateResultAsync',
  'export async function clearAuthorizedAccountBindingFailureStateByContextAsync'
)
const authorizedClearAsyncSource = sourceBetween(
  'export async function clearAuthorizedAccountBindingFailureStateByContextAsync',
  'function accountRuntimeMutationTable'
)

assert.match(
  cooldownAsyncSource,
  /AND status = \?[\s\S]*AND config_revision = \?/,
  'owner cooldown 异步写回必须在同一条 UPDATE 中校验读取时的状态和 config revision'
)
assert.match(
  cooldownAsyncSource,
  /if \(Number\(result\.changes \?\? 0\) <= 0\) \{\s*return undefined\s*\}/,
  'owner cooldown 条件写未命中时必须返回未更新，不能把最新账户摘要误报为写入成功'
)
assert.match(
  exceptionAsyncSource,
  /AND status = \?[\s\S]*AND config_revision = \?/,
  'owner exception 异步写回必须在同一条 UPDATE 中校验读取时的状态和 config revision'
)
assert.match(
  exceptionAsyncSource,
  /current\.status === 'error'/,
  'owner exception 迟到任务已读到人工 error 时必须直接跳过，不能覆盖人工错误详情'
)
assert.match(
  exceptionAsyncSource,
  /if \(Number\(result\.changes \?\? 0\) <= 0\) \{\s*return undefined\s*\}/,
  'owner exception 条件写未命中时必须返回未更新'
)
assert.match(
  authorizedCooldownAsyncSource,
  /AND status NOT IN \('disabled', 'error'\)/,
  'authorized binding 的既有 hard-state 原子保护必须保持不变'
)
for (const [name, source] of [
  ['owner', ownerClearAsyncSource],
  ['authorized binding', authorizedClearAsyncSource]
] as const) {
  assert.match(source, /allowExplicitPolicyRestore/, `${name} PG clear 必须要求显式策略恢复授权`)
  assert.match(source, /COALESCE\(last_error_code, ''\) = \?/, `${name} PG clear 必须在 UPDATE 内原子校验显式 provenance`)
  assert.match(source, /LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX/, `${name} PG clear 必须兼容升级前显式 cooldown`)
}

console.log('owner account runtime mutation fencing 回归通过')

function functionSource(startName: string, nextName: string): string {
  const start = repositorySource.indexOf(`export async function ${startName}`)
  const end = repositorySource.indexOf(`\nexport function ${nextName}`, start)
  assert(start >= 0 && end > start, `无法定位 ${startName} 函数源码`)
  return repositorySource.slice(start, end)
}

function sourceBetween(startMarker: string, endMarker: string): string {
  const start = repositorySource.indexOf(startMarker)
  const end = repositorySource.indexOf(endMarker, start + startMarker.length)
  assert(start >= 0 && end > start, `无法定位 ${startMarker}`)
  return repositorySource.slice(start, end)
}
