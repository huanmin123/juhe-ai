import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const repositorySource = readFileSync(new URL('../../storage/account-runtime-mutation.repository.ts', import.meta.url), 'utf8')

assert.match(
  repositorySource,
  /function normalizedRuntimeObservationAt\(value: string\): string \{\s*return requiredRfc3339Instant\(value, '账户运行态 observedAt'\)/,
  'IPC observedAt 必须严格 RFC3339，不能把无效值静默折叠成 accepted:false'
)

const cooldownAsyncSource = functionSource('markAccountCooldownAsync', 'migrateAccountTraffic')
const exceptionAsyncSource = functionSource('markAccountExceptionAsync', 'markAccountDisabledByFailure')
const authorizedCooldownAsyncSource = functionSource('markAuthorizedAccountBindingCooldownByContextAsync', 'markAuthorizedAccountBindingDisabledByFailure')
const systemQuotaPrioritySource = sourceBetween('function systemQuotaCooldownPrioritySql', 'function systemQuotaCooldownPriorityParams')
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
  /if \(Number\(result\.changes \?\? 0\) <= 0\) return false[\s\S]*if \(!changed\) \{\s*return undefined\s*\}/,
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
assert.match(
  cooldownAsyncSource,
  /systemQuotaCooldownPrioritySql\(failureCode, 'accounts'\)/,
  'owner quota 冷却必须在账户级写回中保留显式 reset/已有通用冷却的优先级门禁'
)
assert.match(
  cooldownAsyncSource,
  /UPDATE \$\{accountRuntimeMutationTable\(tx, 'accounts'\)\} AS accounts/,
  'owner quota 冷却 PG UPDATE 必须为优先级 guard 提供 accounts 别名'
)
assert.match(
  systemQuotaPrioritySource,
  /cooldown_until::timestamptz > \?::timestamptz/,
  'owner quota 冷却 PG 优先级 guard 必须按 timestamptz 比较未来冷却时间'
)
assert.match(
  systemQuotaPrioritySource,
  /julianday\(\$\{prefix\}cooldown_until\) > julianday\(\?\)/,
  'owner quota 冷却 SQLite 优先级 guard 必须按瞬时语义比较未来冷却时间'
)
assert.match(
  authorizedCooldownAsyncSource,
  /systemQuotaCooldownPrioritySql\(input\.failureCode, 'accounts'\)/,
  'authorized quota 冷却必须在账户级写回中保留显式 reset/已有通用冷却的优先级门禁'
)
assert(cooldownAsyncSource.includes('systemQuotaCooldownPriorityParams('), 'owner quota 冷却必须绑定门禁参数')
assert(authorizedCooldownAsyncSource.includes('systemQuotaCooldownPriorityParams('), 'authorized quota 冷却必须绑定门禁参数')
assert.match(
  repositorySource,
  /last_error_code IN \(\?, \?, \?\)[\s\S]*last_error_code IS NULL[\s\S]*last_error_message[\s\S]*LIKE \?/,
  '账户级通用额度写回必须兼容新旧显式 cooldown provenance'
)
assert(repositorySource.includes('LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX'), '账户级通用额度写回必须绑定旧显式 provenance 参数')
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
