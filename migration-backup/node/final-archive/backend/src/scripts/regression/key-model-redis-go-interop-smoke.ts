import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { resolve } from 'node:path'

const redisUrl = process.env.JUHE_AI_REDIS_STATE_URL?.trim()
assert(redisUrl, 'Key-model Redis/Go interop smoke 需要 JUHE_AI_REDIS_STATE_URL')

const namespace = `dev-km-interop-${process.pid}-${Date.now()}`
const sourceId = `dev-km-interop-source-${process.pid}`
process.env.JUHE_AI_REDIS_NAMESPACE = namespace

const jobsRoot = resolve(import.meta.dirname, '../../../../backend-go/projects/jobs')
const {
  RedisKeyModelRuntimeStore
} = await import('../../modules/gateway/runtime/key-model-redis-store.js')
const { capabilityHash } = await import('../../modules/gateway/runtime/key-model-runtime.js')
const {
  closeRedisClients,
  createDedicatedRedisClient
} = await import('../../shared/redis-client.js')

const capability = {
  credentialSourceAccountId: sourceId,
  keyFingerprint: 'node-go-interop-key-fingerprint',
  clientModel: 'gpt-node-go-interop',
  clientEndpointFamily: 'chat_completions',
  finalUpstreamModel: 'gpt-node-go-interop',
  upstreamEndpointMode: 'chat_json',
  dispatchRevision: 1
} as const
const hash = capabilityHash(capability)
const store = new RedisKeyModelRuntimeStore(redisUrl)
const cleanupClient = await createDedicatedRedisClient(redisUrl)
let permit: { capabilityHash: string; attemptId: string; leaseUntilMs: number } | undefined

try {
  assert.equal(
    store.keys.due,
    `juhe-ai:${namespace}:gateway-account-circuit-key-model:due`,
    'Node 必须在传给 Go 的同一隔离 Redis namespace 写入 Key-model state'
  )
  const recorded = await store.recordFailure({
    intentId: `node-go-interop-intent-${process.pid}`,
    requestId: `node-go-interop-request-${process.pid}`,
    attemptId: `node-go-interop-attempt-${process.pid}`,
    capability,
    observedAtMs: Date.now(),
    outcome: 'upstream_not_complete',
    sourceFence: `node-go-interop-fence-${process.pid}`
  })
  assert.equal(recorded.status, 'applied')
  assert.equal(recorded.state.phase, 'OPEN')
  assert.equal(recorded.state.backoffAttempt, 1)
  const retryAtMs = recorded.state.retryAtMs
  assert(retryAtMs, 'Node Redis state 必须持久化首次 5 秒 OPEN retryAt')
  await delay(Math.max(0, retryAtMs - Date.now()) + 100)

  const go = await runGoFixture({
    JUHE_AI_KEY_MODEL_REDIS_INTEROP_URL: redisUrl,
    JUHE_AI_KEY_MODEL_REDIS_INTEROP_NAMESPACE: namespace,
    JUHE_AI_KEY_MODEL_REDIS_INTEROP_SOURCE_ID: sourceId
  })
  assert.equal(go.code, 0, `${go.stdout}\n${go.stderr}`)

  const closed = await store.get(capability)
  assert(closed, 'Go 连续三次恢复成功后 Node 必须读取到 state')
  assert.equal(closed.phase, 'CLOSED')
  assert.equal(closed.recoverySuccessCount, 0)
  assert.equal(closed.lastRecoverySuccessAtMs, undefined)

  const admission = await store.admitForeground(capability, `node-go-interop-readmit-${process.pid}`)
  assert.equal(admission.status, 'admitted', 'CLOSED 后 Node 必须恢复 Key-model 放行')
  if (admission.status === 'admitted') permit = admission.permit

  console.log(JSON.stringify({
    event: 'key_model_redis_go_interop_smoke_passed',
    namespace,
    capabilityHash: hash,
    recoverySuccessThreshold: 3
  }))
} finally {
  if (permit) await store.releaseForeground(permit).catch(() => false)
  await deleteFixtureKeys(cleanupClient, store, hash, sourceId)
  if (cleanupClient.quit) await cleanupClient.quit().catch(() => undefined)
  else cleanupClient.destroy?.()
  await closeRedisClients()
}

async function delay(ms: number): Promise<void> {
  await new Promise<void>((resolveDelay) => setTimeout(resolveDelay, ms))
}

async function runGoFixture(overrides: Record<string, string>): Promise<{ code: number | null; stdout: string; stderr: string }> {
  return await new Promise((resolveRun, rejectRun) => {
    const child = spawn(process.env.JUHE_AI_GO_BINARY?.trim() || 'go', [
      'test', './internal/keymodelrecovery', '-run', '^TestNodeRedisRecoveryInteropFixture$', '-count=1'
    ], {
      cwd: jobsRoot,
      env: { ...process.env, ...overrides },
      stdio: ['ignore', 'pipe', 'pipe']
    })
    let stdout = ''
    let stderr = ''
    child.stdout.setEncoding('utf8').on('data', (chunk: string) => { stdout += chunk })
    child.stderr.setEncoding('utf8').on('data', (chunk: string) => { stderr += chunk })
    child.once('error', rejectRun)
    child.once('close', (code) => resolveRun({ code, stdout, stderr }))
  })
}

async function deleteFixtureKeys(
  client: Awaited<ReturnType<typeof createDedicatedRedisClient>>,
  fixtureStore: InstanceType<typeof RedisKeyModelRuntimeStore>,
  capabilityHashValue: string,
  credentialSourceAccountId: string
): Promise<void> {
  const keys = fixtureStore.keys
  const prefix = keys.due.replace(/:due$/, '')
  const sourceDigest = createHash('sha256').update(credentialSourceAccountId).digest('hex')
  const stateKey = keys.state(capabilityHashValue)
  await client.sendCommand(['DEL',
    stateKey,
    stateKey.replace(':state:', ':lease:'),
    keys.due,
    keys.closed,
    keys.receipt(`node-go-interop-intent-${process.pid}`),
    keys.admission(capabilityHashValue),
    keys.admissionLease(capabilityHashValue, `node-go-interop-readmit-${process.pid}`),
    keys.admissionWake(capabilityHashValue),
    keys.mainProbeFence(capabilityHashValue),
    keys.admissionEvents,
    keys.capacity,
    `${prefix}:recovery:global`,
    `${prefix}:recovery:source:${sourceDigest}`
  ]).catch(() => undefined)
}
