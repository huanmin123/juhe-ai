import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { detectAccountBalanceAdapter } from '../../modules/background/account-balance-auto-detect.service.js'
import { queryBuiltinAccountBalance } from '../../modules/accounts/account-balance-query.service.js'
import type { AccountBalanceDetectionCandidate } from '../../storage/account-balance.repository.js'

const candidate: AccountBalanceDetectionCandidate = {
  id: 'account-auto-detect',
  systemAccountId: 'system-owner',
  configRevision: 7,
  credentials: { api_key: 'sk-test', base_url: 'https://relay.example/v1' },
  proxyProfileId: 'proxy-required'
}

const preferredAttempts: string[] = []
const resolved = await queryBuiltinAccountBalance({
  ...candidate,
  config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'newapi' }
}, {
  queryAdapter: async (_input, adapter) => {
    preferredAttempts.push(adapter)
    if (adapter === 'newapi') return { status: 'unsupported', errorMessage: 'saved preference no longer matches' }
    if (adapter === 'sub2api') return { status: 'fresh', remainingUsd: '8.000000' }
    throw new Error('should stop at the first fallback success')
  }
})
assert.deepEqual(preferredAttempts, ['newapi', 'sub2api'], '内置适配必须先尝试偏好，失败后从完整规则回退')
assert.equal(resolved.adapter, 'sub2api')

const attempts: string[] = []
const detected = await detectAccountBalanceAdapter(candidate, {
  queryBuiltin: async (input) => {
    assert.equal(input.proxyProfileId, 'proxy-required', '自动探测必须保留账户绑定代理')
    attempts.push('builtin')
    return {
      adapter: 'litellm',
      snapshot: { status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'budget' }
    }
  }
})
assert.deepEqual(attempts, ['builtin'])
assert.deepEqual(detected, {
  config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'litellm' },
  snapshot: { status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'budget' }
})

const unlimited = await detectAccountBalanceAdapter(candidate, {
  queryBuiltin: async () => ({
    adapter: 'user_balance',
    snapshot: { status: 'unlimited', basis: 'api_key_quota' }
  })
})
assert.deepEqual(unlimited, {
  config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
  snapshot: { status: 'unlimited', basis: 'api_key_quota' }
}, '账户级 unlimited 应作为严格命中自动开启')

for (const snapshot of [
  { status: 'unsupported', errorMessage: 'all adapters unsupported' },
  { status: 'failed', errorMessage: 'upstream failed' },
  { status: 'pending', consecutiveTransientFailures: 1 }
] as const) {
  const unsupported = await detectAccountBalanceAdapter(candidate, {
    queryBuiltin: async () => ({ adapter: 'user_balance', snapshot })
  })
  assert.equal(unsupported, undefined, `业务状态 ${snapshot.status} 不能自动开启`)
}

const rejected = await detectAccountBalanceAdapter(candidate, {
  queryBuiltin: async () => { throw new Error('upstream request failed') }
})
assert.equal(rejected, undefined, '查询异常不能自动开启')

const healthSource = readFileSync(resolve('src/modules/background/account-health-check.service.ts'), 'utf8')
assert.match(healthSource, /reason === 'activation'[\s\S]*account\.status === 'pending_test'[\s\S]*account\.status === 'active'/, '首次创建健康检查成功后应允许待检查和直接启用的单 Key 账户成为余额自动探测候选')
assert.match(healthSource, /const scheduleBalanceAutoDetection = shouldScheduleAccountBalanceAutoDetection\(account, item\.reason\)[\s\S]*if \(changed && scheduleBalanceAutoDetection\) \{[\s\S]*enqueueAccountBalanceAutoDetection/, '健康检查写回成功后才能投递余额自动探测')

const querySource = readFileSync(resolve('src/modules/accounts/account-balance-query.service.ts'), 'utf8')
assert.match(querySource, /resolveProxyUrlForProfileAsync\(candidate\.proxyProfileId\)/, '所有余额查询路径必须解析账户绑定代理')

const backfillSource = readFileSync(resolve('src/scripts/maintenance/backfill-account-balance-auto-detect.ts'), 'utf8')
assert.match(backfillSource, /afterId/, '全量余额探测必须使用 ID 游标分页')
assert.match(backfillSource, /concurrency = runtimeConfig\.concurrency\.globalMax/, '全量余额探测必须使用全局共享并发池')
assert.match(backfillSource, /runWithGlobalBackgroundConcurrencySlot/, '全量余额探测必须获取全局共享槽')
assert.match(backfillSource, /await closeRedisClients\(\)/, '全量余额探测完成后必须关闭 Redis 客户端，避免维护进程挂起')
assert.ok(
  backfillSource.indexOf('await migrateLegacyAccountBalanceConfigurations()') < backfillSource.lastIndexOf('listAccountBalanceDetectionCandidatePageAsync'),
  '上线扫描必须先离线转换旧适配器配置，再扫描未开启账户'
)

console.log('account balance auto detect regression passed')
