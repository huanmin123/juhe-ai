import assert from 'node:assert/strict'

import {
  RedisKeyModelRuntimeStore,
  admitKeyModelForegroundScript,
  recordKeyModelFailureScript,
  releaseKeyModelForegroundScript
} from '../../modules/gateway/runtime/key-model-redis-store.js'
import type { RedisCommandClient } from '../../shared/redis-client.js'
import type { CapabilityKey } from '../../modules/gateway/runtime/key-model-runtime.js'

assert.equal(recordKeyModelFailureScript.includes('ARGV[7]'), false, '失败封口脚本不得读取不存在的 ARGV[7]')
assert.match(recordKeyModelFailureScript, /ZREM', KEYS\[5\], ARGV\[5\]/, '失败封口必须移除 permit 的 attemptId 成员')

const values = new Map<string, string>()
const expirations = new Map<string, number>()
const sortedSets = new Map<string, Map<string, number>>()
let execution = Promise.resolve()
const atomic = <T>(operation: () => T): Promise<T> => {
  const result = execution.then(operation)
  execution = result.then(() => undefined, () => undefined)
  return result
}
const client: RedisCommandClient = {
  connect: async () => undefined,
  on: () => undefined,
  get: async (key) => activeValue(key),
  set: async (key, value) => { values.set(key, value); return 'OK' },
  del: async (key) => values.delete(key) ? 1 : 0,
  sendCommand: async () => undefined,
  eval: async (script, input) => atomic(() => emulate(script, input.keys, input.arguments))
}

const capability: CapabilityKey = {
  credentialSourceAccountId: 'source-1', keyFingerprint: 'key-1', clientModel: 'B', clientEndpointFamily: 'chat_completions', finalUpstreamModel: 'B-upstream', upstreamEndpointMode: 'chat_json', dispatchRevision: 1
}
const store = new RedisKeyModelRuntimeStore(
  'redis://test.invalid',
  async () => client,
  async (script, keys, args) => client.eval(script, { keys, arguments: args })
)
const now = Date.now()
const decisions = await Promise.all([...Array(10)].map((_, index) => store.admitForeground(capability, `attempt-${index}`)))
assert.equal(decisions.filter((decision) => decision.status === 'admitted').length, 2, '10 并发只能原子取得 2 个 permit')
assert.equal(decisions.filter((decision) => decision.status === 'busy').length, 8)
const admitted = decisions.filter((decision): decision is Extract<typeof decision, { status: 'admitted' }> => decision.status === 'admitted')
assert.equal(await store.releaseForeground(admitted[0]!.permit), true)
const afterRelease = await store.admitForeground(capability, 'attempt-after-release')
assert.equal(afterRelease.status, 'admitted', '释放后必须唤醒并归还容量')
assert.equal(await store.releaseForeground(admitted[1]!.permit), true)

const intent = { intentId: 'intent-1', requestId: 'request-1', attemptId: 'failure-attempt', capability, observedAtMs: now, outcome: 'upstream_not_complete' as const, sourceFence: 'source-fence-1', permit: afterRelease.status === 'admitted' ? afterRelease.permit : undefined }
const opened = await store.recordFailure(intent)
assert.equal(opened.status, 'applied')
assert.equal(opened.status === 'applied' ? opened.state.retryAtMs : undefined, now + 5_000)
if (opened.status === 'applied') {
  assert.equal(sortedSets.get(store.keys.admission(opened.state.capabilityHash))?.size ?? 0, 0, '失败封口必须移除 permit 的 attemptId 成员')
}
assert.equal((await store.recordFailure(intent)).status, 'idempotent')
assert.equal((await store.admitForeground(capability, 'blocked-attempt')).status, 'blocked')

function activeValue(key: string): string | null {
  const expiry = expirations.get(key)
  if (expiry !== undefined && expiry <= Date.now()) { values.delete(key); expirations.delete(key) }
  return values.get(key) ?? null
}

function emulate(script: string, keys: string[], args: string[]): unknown[] {
  if (script === admitKeyModelForegroundScript) {
    const existing = activeValue(keys[2]!)
    if (existing) return ['idempotent', activeValue(keys[3]!) ?? '0', existing]
    const rawState = activeValue(keys[0]!)
    if (rawState && JSON.parse(rawState).phase !== 'CLOSED') return ['blocked', activeValue(keys[3]!) ?? '0', '0']
    const permits = sortedSets.get(keys[1]!) ?? new Map<string, number>()
    for (const [member, score] of permits) if (score <= now) permits.delete(member)
    sortedSets.set(keys[1]!, permits)
    if (permits.size >= Number(args[2])) return ['busy', activeValue(keys[3]!) ?? '0', '0']
    const leaseUntil = now + Number(args[1])
    values.set(keys[2]!, String(leaseUntil)); expirations.set(keys[2]!, now + Number(args[1]))
    permits.set(args[0]!, leaseUntil)
    return ['admitted', activeValue(keys[3]!) ?? '0', String(leaseUntil)]
  }
  if (script === releaseKeyModelForegroundScript) {
    if (!values.delete(keys[1]!)) return [0, activeValue(keys[2]!) ?? '0']
    sortedSets.get(keys[0]!)?.delete(args[1]!)
    const wake = Number(activeValue(keys[2]!) ?? '0') + 1; values.set(keys[2]!, String(wake))
    return [1, wake]
  }
  if (script === recordKeyModelFailureScript) {
    const releasePermit = () => {
      if (!values.delete(keys[5]!)) return
      sortedSets.get(keys[4]!)?.delete(args[4]!)
      values.set(keys[6]!, String(Number(activeValue(keys[6]!) ?? '0') + 1))
    }
    const receipt = activeValue(keys[2]!)
    if (receipt) { releasePermit(); return ['idempotent', activeValue(keys[0]!) ?? receipt] }
    const incoming = JSON.parse(args[0]!) as Record<string, unknown>
    incoming.lastObservedAtMs = now
    incoming.retryAtMs = now + 5_000
    const currentRaw = activeValue(keys[0]!)
    if (currentRaw) {
      const current = JSON.parse(currentRaw) as Record<string, unknown>
      if (Number(current.dispatchRevision) > Number(args[1])) { releasePermit(); return ['stale', ''] }
      if (Number(current.dispatchRevision) === Number(args[1]) && current.phase !== 'CLOSED') {
        current.lastObservedAtMs = now; current.lastOutcome = 'upstream_not_complete'
        const encoded = JSON.stringify(current); values.set(keys[0]!, encoded); values.set(keys[2]!, encoded); releasePermit(); return ['idempotent', encoded]
      }
      incoming.generation = Number(current.generation ?? 0) + 1
    } else {
      values.set(keys[3]!, String(Number(activeValue(keys[3]!) ?? '0') + 1))
    }
    const encoded = JSON.stringify(incoming); values.set(keys[0]!, encoded); values.set(keys[2]!, encoded); releasePermit()
    return ['applied', encoded]
  }
  throw new Error('unexpected script')
}

console.log('key-model-redis-store regression passed')
