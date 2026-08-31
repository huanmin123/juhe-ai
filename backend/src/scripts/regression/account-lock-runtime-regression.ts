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
assert.match(upstreamDispatch, /waitForAccountLockDelay\(/, '锁死租约等待必须统一走受预算约束的等待入口')
assert.match(upstreamDispatch, /routeCoordinationBudget\.remainingMs/, '锁死租约等待必须受 routeCoordinationBudget 约束')
assert.match(upstreamDispatch, /if \(error instanceof GatewayRequestWallBudgetExhaustedError\) \{\s*throw error/, '锁死等待预算终止必须直接交回路由层，不能误记为传输失败')
assert.match(upstreamDispatch, /try \{\s*while \(dispatchAccounts\.length > 0\)/, '派发器异常退出必须统一清理在途锁死租约')
assert.match(upstreamDispatch, /leaseId: activeAccountLockRetryLease\??\.accountId === account\.id[\s\S]*?activeAccountLockRetryLease\.leaseId/, '锁死 attempt observation 必须携带当前 lease fence')
assert.match(upstreamDispatch, /if \(!lockLease\.leaseId\) \{[\s\S]*?acquireAccountLockRetryLeaseAsync\(accountId, configuredDelayMs\)[\s\S]*?consumeAccountLockRetryLeaseAsync\(accountId, lockLease\.leaseId\)/, '共享到期时间无 leaseId 时必须重新 CAS 领取发送租约')
const gatewayRoutes = readFileSync(resolve('src/modules/gateway/routes.ts'), 'utf8')
assert.ok(!gatewayRoutes.includes("recordAccountLockFailureAsync(error.accountId, 'speed_first_cutover_denied')"), 'speed_first 首字慢不得开启锁死事故')
assert.ok(!gatewayRoutes.includes("recordAccountLockFailureAsync(candidate.id, 'upstream_attempt_exhausted')"), '账户尝试耗尽不得伪造锁死证据')
assert.match(gatewayRoutes, /accountLockTrafficEnabled && accountLockBlocksCrossAccount\(lockState\)/, 'speed_first 仅在活动锁死窗口内保留同账号')
assert.match(gatewayRoutes, /const accountLockTrafficEnabled = trafficSource === 'gateway' && options\.disableAccountStateMutation !== true/, '禁用账户状态变更时不得执行锁死副作用')
assert.match(gatewayRoutes, /deadConfirmedAccountIds/, '死期结算后的账户必须从当前请求候选中移除')
assert.match(gatewayRoutes, /accountLockRetryLease: dispatchAccountLockRetryLease/, '路由必须把消费后的锁死租约传给实际派发')
assert.match(gatewayRoutes, /releaseAccountLockRetryLeaseAsync\(\{/, '实际派发异常必须释放锁死在途租约')
assert.match(gatewayRoutes, /pendingSameAccountRetry[\s\S]*pendingRetryLockedAccount/, '锁死同账号重试仍必须经过锁死租约闸门')
const repository = readFileSync(resolve('src/storage/account-lock.repository.ts'), 'utf8')
assert.match(repository, /lease_until_ms > \?/, '消费锁死重试租约时必须拒绝过期持有者')
assert.match(repository, /export interface AccountLockObservation/, '锁死结算必须支持 generation/incident 观察围栏')
assert.match(repository, /leaseId\?: string \| null/, '锁死观察围栏必须可绑定在途 lease')
assert.match(repository, /lease_id = \? AND lease_until_ms > \?/, '锁死观察事件必须拒绝已过期的在途 lease')
assert.match(repository, /if \(!leaseId\) return false/, '消费锁死租约不得接受 undefined leaseId 伪成功')
assert.match(upstreamDispatch, /if \(lockLease\.leaseId\) \{[\s\S]*?consumeAccountLockRetryLeaseAsync\(accountId, lockLease\.leaseId\)/, '无锁或 LOCKED_IDLE 普通重试不得伪造 lease 消费')
assert.match(repository, /releaseAccountLockRetryLeaseAsync/, '锁死执行租约必须支持 attempt 完成后的显式释放')
assert.match(repository, /abandonAccountLockRetryReservationAsync/, '交接或取消必须释放等待预约而保留共享到期时间')
assert.match(repository, /accountLockDispatchLeaseDurationMs/, '锁死执行租约必须覆盖在途 attempt 生命周期')
assert.match(repository, /\.generation = \?/, '锁死配置 UPSERT 必须以 generation CAS 防止覆盖运行态迁移')
assert.match(repository, /recoveryResult\.changes === 1 \? recovered : findAccountLockStateAsync/, '自动恢复 CAS 失败不得返回伪快照')
console.log('account-lock-runtime-regression: ok')
