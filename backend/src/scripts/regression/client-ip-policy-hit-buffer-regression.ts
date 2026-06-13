import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { ActiveClientIpPolicy } from '../../storage/client-ip-stats.repository.js'
import {
  getClientIpPolicyCacheRuntime,
  recordClientIpPolicyHitAsync
} from '../../modules/gateway/runtime/client-ip-policy-cache.service.js'

runtimeConfig.processRole = 'server'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

assertClientIpPolicyHitBufferSourceGuards()

const initialRuntime = getClientIpPolicyCacheRuntime()
assert.equal(initialRuntime.maxPendingPolicyLoads, 1024, 'IP 封禁策略 cache miss 并发查询必须有固定上限')
assert.equal(initialRuntime.droppedPolicyLoadCount, 0, '初始 IP 封禁策略查询溢出计数应为 0')
const maxPendingHits = initialRuntime.maxPendingPolicyHits
for (let index = 0; index < maxPendingHits + 25; index += 1) {
  recordClientIpPolicyHitAsync(policyForIndex(index))
}

const overflowRuntime = getClientIpPolicyCacheRuntime()
assert.equal(overflowRuntime.pendingPolicyHitCount, maxPendingHits, 'IP 封禁命中待写缓冲必须按固定 distinct key 上限截断')
assert.equal(overflowRuntime.droppedPolicyHitCount, 25, '超过固定窗口的新 distinct 命中应丢弃并计数，不能继续扩大 server 内存')
assert.equal(overflowRuntime.flushBatchSize, 1000, '单次投递 DB service 的 IP 封禁命中应固定批量，避免一次大 IPC')

recordClientIpPolicyHitAsync(policyForIndex(0))
const mergedRuntime = getClientIpPolicyCacheRuntime()
assert.equal(mergedRuntime.pendingPolicyHitCount, maxPendingHits, '同一 IP/策略命中应在已有 key 上合并，不应增加 distinct 缓冲项')
assert.equal(mergedRuntime.droppedPolicyHitCount, 25, '同一 key 合并不应被当成新溢出')

console.log('IP 封禁命中缓冲回归通过：server 内存缓冲和单次 DB service 投递都有固定窗口，极端多来源命中不会放大成无界 Map 或大 IPC')

function policyForIndex(index: number): ActiveClientIpPolicy {
  return {
    id: 'policy_buffer_guard',
    ipHash: `ip_hash_${index}`,
    aggregateIpKey: `10.0.${Math.floor(index / 255)}.${index % 255}`,
    clientIp: `10.0.${Math.floor(index / 255)}.${index % 255}`,
    reason: 'buffer guard'
  }
}

function assertClientIpPolicyHitBufferSourceGuards(): void {
  const source = readFileSync(new URL('../../modules/gateway/runtime/client-ip-policy-cache.service.ts', import.meta.url), 'utf8')
  assert(source.includes('clientIpPolicyLoadMaxPendingEntries'), 'IP 封禁策略查询必须声明固定 pending load 上限')
  assert(source.includes('pendingPolicyLoads.size >= clientIpPolicyLoadMaxPendingEntries'), '新增 IP 封禁策略查询前必须检查 pending load 上限')
  assert(source.includes("event: 'client_ip_policy_load_dropped'"), 'IP 封禁策略查询超过 pending load 上限时必须记录丢弃计数和日志')
  assert(source.includes("status: 'skipped'"), 'IP 封禁策略查询过载跳过必须有独立状态，不能混同为未命中策略')
  assert(source.includes("loaded.status !== 'loaded'"), '只有真实完成的 IP 封禁策略查询结果才能写入短 TTL 缓存')
  assert(source.includes('clientIpPolicyHitMaxPendingEntries'), 'IP 封禁命中缓冲必须声明固定 distinct key 上限')
  assert(source.includes('pendingPolicyHits.size >= clientIpPolicyHitMaxPendingEntries'), '新增 distinct 命中入队前必须检查缓冲上限')
  assert(source.includes('clientIpPolicyHitFlushBatchSize'), 'IP 封禁命中 flush 必须声明固定批量')
  assert(source.includes('slice(0, clientIpPolicyHitFlushBatchSize)'), '单次 flush 只能取固定窗口，不能把全部待写命中一次性发给 DB service')
}
