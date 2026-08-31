import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  accountLockBlocksCrossAccount,
  normalizeAccountLockDeathTimeoutSeconds,
  normalizeAccountLockRetryIntervalSeconds,
  sampleLockDelayMs
} from '../../storage/account-lock.repository.js'

assert.equal(normalizeAccountLockDeathTimeoutSeconds(undefined), 300)
assert.equal(normalizeAccountLockRetryIntervalSeconds(undefined), 5)
assert.throws(() => normalizeAccountLockDeathTimeoutSeconds(29))
assert.throws(() => normalizeAccountLockRetryIntervalSeconds(31))
for (const base of [5, 10, 11, 30]) {
  const value = sampleLockDelayMs(base, `seed-${base}`)
  assert.ok(value >= 2000 && value <= 30000)
}
assert.equal(accountLockBlocksCrossAccount({
  accountId: 'a', enabled: true, lockState: 'ENGAGED', lockDeathTimeoutSeconds: 300,
  lockRetryIntervalSeconds: 5, generation: 1, deadlineAt: new Date(Date.now() + 10000).toISOString(), updatedAt: new Date().toISOString()
}), true)
assert.equal(accountLockBlocksCrossAccount({
  accountId: 'a', enabled: true, lockState: 'DEAD_CONFIRMED', lockDeathTimeoutSeconds: 300,
  lockRetryIntervalSeconds: 5, generation: 1, updatedAt: new Date().toISOString()
}), false)
const schema = readFileSync(resolve('src/storage/schema/business-schema.ts'), 'utf8')
assert.match(schema, /CREATE TABLE IF NOT EXISTS account_lock_states/)
const routes = readFileSync(resolve('src/modules/accounts/account-lock.routes.ts'), 'utf8')
assert.match(routes, /\/\:id\/lock/)
const upstreamDispatch = readFileSync(resolve('src/modules/gateway/dispatch/upstream-dispatch.ts'), 'utf8')
assert.ok(!upstreamDispatch.includes("recordAccountLockFailureAsync(account.id, 'upstream_http_response')"), '完整 HTTP 响应不得直接开启锁死事故')
assert.match(upstreamDispatch, /accountLockTrafficEnabled && accountId && reason === 'upstream_transport_failure'/, '仅 gateway transport 失败可开启锁死事故')
assert.match(upstreamDispatch, /if \(!lockRetryScheduled && configuredDelayMs > 0\)/, '锁未启用或 LOCKED_IDLE 时必须保留原同账号重试间隔')
assert.match(upstreamDispatch, /const accountLockTrafficEnabled = accountStateMutationEnabled && usageContext\.trafficSource === 'gateway'/, '手工测试和后台探针不得使用锁死状态副作用')
const gatewayRoutes = readFileSync(resolve('src/modules/gateway/routes.ts'), 'utf8')
assert.ok(!gatewayRoutes.includes("recordAccountLockFailureAsync(error.accountId, 'speed_first_cutover_denied')"), 'speed_first 首字慢不得开启锁死事故')
assert.ok(!gatewayRoutes.includes("recordAccountLockFailureAsync(candidate.id, 'upstream_attempt_exhausted')"), '账户尝试耗尽不得伪造锁死证据')
assert.match(gatewayRoutes, /accountLockTrafficEnabled && accountLockBlocksCrossAccount\(lockState\)/, 'speed_first 仅在活动锁死窗口内保留同账号')
assert.match(gatewayRoutes, /const accountLockTrafficEnabled = trafficSource === 'gateway' && options\.disableAccountStateMutation !== true/, '禁用账户状态变更时不得执行锁死副作用')
console.log('account-lock-runtime-regression: ok')
