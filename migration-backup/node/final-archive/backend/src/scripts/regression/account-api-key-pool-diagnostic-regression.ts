import assert from 'node:assert/strict'

import {
  accountApiKeyPoolKeySetFingerprint,
  orderAccountApiKeyPoolEntries,
  runAccountApiKeyPoolDiagnostic
} from '../../modules/accounts/account-api-key-pool-diagnostic.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'

const keys = ['sk-pool-diagnostic-0', 'sk-pool-diagnostic-1', 'sk-pool-diagnostic-2', 'sk-pool-diagnostic-3', 'sk-pool-diagnostic-4']
const entries = accountApiKeyEntries({ api_key: keys[0], api_keys: keys })
const candidate = {
  id: 'pool-diagnostic-regression',
  providerCode: 'gpt',
  providerProtocolProfileId: 'gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  apiKey: keys[0],
  apiKeys: keys,
  credentials: { api_key: keys[0], api_keys: keys, base_url: 'https://api.openai.com/v1' }
} as unknown as OpenAIAccountSecret

assert.equal(
  accountApiKeyPoolKeySetFingerprint(entries),
  accountApiKeyPoolKeySetFingerprint([...entries].reverse()),
  'Key 集合指纹必须不受原始排序影响'
)
assert.deepEqual(
  orderAccountApiKeyPoolEntries(entries, entries[1]!.fingerprint).map((entry) => entry.key),
  [keys[2], keys[3], keys[4], keys[0], keys[1]],
  '下一轮必须从持久化游标 Key 的后一个开始'
)

let inFlight = 0
let maxInFlight = 0
const calls: Array<{ key: string; timeoutMs: number }> = []
const result = await runAccountApiKeyPoolDiagnostic(candidate, entries, async ({ entry, timeoutMs }) => {
  calls.push({ key: entry.key, timeoutMs })
  inFlight += 1
  maxInFlight = Math.max(maxInFlight, inFlight)
  await new Promise<void>((resolve) => setTimeout(resolve, 5))
  inFlight -= 1
  const firstTierTimeout = timeoutMs === 10_000
  const secondTierSuccess = timeoutMs === 20_000 && entry.key === keys[1]
  return {
    value: { key: entry.key, timeoutMs },
    success: secondTierSuccess,
    timedOutAfterRealUpstreamAttempt: firstTierTimeout
  }
})

assert(result, '多 Key 候选必须进入统一诊断器')
assert.equal(maxInFlight, entries.length, '管理页面主动发起的 API Key 池诊断不得被后台并发门禁排队')
assert(calls.some((call) => call.timeoutMs === 10_000), '首轮必须使用 10 秒超时')
assert(calls.some((call) => call.timeoutMs === 20_000), '仅真实上游超时的 Key 必须进入 20 秒复测')
assert.equal(calls.some((call) => call.timeoutMs === 30_000), false, '20 秒阶段出现成功后不得继续派发 30 秒阶段')
assert.equal(result.winner?.value.key, keys[1], '任一固定 Key 成功必须作为整轮成功结果')
assert.equal(result.lastCompletedFingerprint, entries[1]?.fingerprint, '成功前连续完成的 Key 才能推进下一轮游标')
assert.equal(result.completed, false, '成功提前结束时未完成复测的 Key 必须保留到下一轮')

const partialAfterError = await runAccountApiKeyPoolDiagnostic(candidate, entries.slice(0, 3), async ({ entry }) => {
  if (entry.key === keys[1]) throw new Error('模拟单 Key 调用异常')
  return {
    value: entry.key,
    success: false,
    timedOutAfterRealUpstreamAttempt: false
  }
})
assert.equal(partialAfterError?.errors.length, 1, '单 Key 调用异常必须保留到聚合结果，不能中断已完成 Key 的游标处理')
assert.equal(partialAfterError?.lastCompletedFingerprint, entries[0]?.fingerprint, '游标只能推进到扫描顺序连续完成的 Key')
assert.equal(partialAfterError?.completed, false, '存在调用异常时整轮不得标记完成')

const completedRound = await runAccountApiKeyPoolDiagnostic(candidate, entries, async ({ entry }) => ({
  value: entry.key,
  success: false,
  timedOutAfterRealUpstreamAttempt: false
}))
assert.equal(completedRound?.completed, true, '全部 Key 都完成后必须标记轮次完成，以便下次从头开始')

const singleKey = [entries[0]!]
const singleResult = await runAccountApiKeyPoolDiagnostic(candidate, singleKey, async ({ timeoutMs }) => ({
  value: timeoutMs,
  success: false,
  timedOutAfterRealUpstreamAttempt: false
}), { allowSingleEntry: true })
assert.equal(singleResult?.attempts.length, 1, '单 Key 冷却复测必须进入同一共享诊断器')
assert.equal(singleResult?.attempts[0]?.value, 10_000, '单 Key 也必须从 10 秒档开始')

const singleSuccess = await runAccountApiKeyPoolDiagnostic(candidate, singleKey, async ({ entry }) => ({
  value: entry.key,
  success: true,
  timedOutAfterRealUpstreamAttempt: false
}), { allowSingleEntry: true })
assert.equal(singleSuccess?.completed, true, '唯一 Key 成功后应完成本轮，下一轮必须从池首开始')

const imageScheduleCalls: number[] = []
const imageScheduleResult = await runAccountApiKeyPoolDiagnostic(candidate, entries.slice(0, 2), async ({ timeoutMs }) => {
  imageScheduleCalls.push(timeoutMs)
  return {
    value: timeoutMs,
    success: false,
    timedOutAfterRealUpstreamAttempt: true
  }
}, { timeoutSchedule: [120_000] })
assert.deepEqual(imageScheduleCalls, [120_000, 120_000], '图片 Key 池必须让每个 Key 使用单次 120 秒诊断窗口')
assert.equal(imageScheduleResult?.attempts.length, 2, '图片 Key 池单次窗口结束后不得进入文本重试阶梯')

let imageInFlight = 0
let imageMaxInFlight = 0
await runAccountApiKeyPoolDiagnostic(candidate, entries.slice(0, 3), async ({ entry }) => {
  imageInFlight += 1
  imageMaxInFlight = Math.max(imageMaxInFlight, imageInFlight)
  await new Promise<void>((resolve) => setTimeout(resolve, 5))
  imageInFlight -= 1
  return {
    value: entry.key,
    success: false,
    timedOutAfterRealUpstreamAttempt: false
  }
}, { maxConcurrentAttempts: 1 })
assert.equal(imageMaxInFlight, 1, 'Images API Key 池必须串行发起上游尝试')

let textInFlight = 0
let textMaxInFlight = 0
await runAccountApiKeyPoolDiagnostic(candidate, entries.slice(0, 3), async ({ entry }) => {
  textInFlight += 1
  textMaxInFlight = Math.max(textMaxInFlight, textInFlight)
  await new Promise<void>((resolve) => setTimeout(resolve, 5))
  textInFlight -= 1
  return {
    value: entry.key,
    success: false,
    timedOutAfterRealUpstreamAttempt: false
  }
})
assert.equal(textMaxInFlight, 3, '文本 API Key 池必须保持默认全并发诊断')

const cancellationController = new AbortController()
const cancellationCalls: string[] = []
const canceledDiagnostic = await runAccountApiKeyPoolDiagnostic(candidate, entries.slice(0, 3), async ({ entry, signal }) => {
  cancellationCalls.push(entry.key)
  cancellationController.abort()
  await new Promise<void>((resolve) => setTimeout(resolve, 5))
  assert.equal(signal.aborted, true, '外层取消信号必须传入固定 Key 尝试')
  return {
    value: entry.key,
    success: false,
    timedOutAfterRealUpstreamAttempt: false
  }
}, { signal: cancellationController.signal })
assert.equal(cancellationCalls.length, 1, '取消后不得启动新的 Key 尝试')
assert.equal(canceledDiagnostic?.attempts.length, 0, '取消后的中断结果不得被视为完成尝试')

console.log('account-api-key-pool-diagnostic-regression passed')
