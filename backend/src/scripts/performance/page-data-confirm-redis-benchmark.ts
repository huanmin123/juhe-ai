import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { performance } from 'node:perf_hooks'

import {
  createRedisPageDataChangeStore,
  PAGE_DATA_PROTOCOL_VERSION,
  pageDataScope,
  type PageDataDomain,
  type PageDataRedisClient,
  type PageDataRevisionToken
} from '../../modules/page-data/page-data-change.service.js'
import { createDedicatedRedisClient, type RedisCommandClient } from '../../shared/redis-client.js'

interface CommandCounts {
  eval: number
  get: number
  set: number
  sendCommand: number
}

interface CountingRedisClient extends PageDataRedisClient {
  commandCounts(): CommandCounts
  resetCommandCounts(): void
}

interface BenchmarkResult {
  name: string
  domains: number
  concurrency: number
  confirms: number
  p50Ms: number
  p95Ms: number
  p99Ms: number
  redisCommandsPerConfirm: CommandCounts
}

const redisUrlEnvironmentName = 'JUHE_AI_PAGE_DATA_CONFIRM_BENCHMARK_REDIS_URL'
const iterations = positiveInteger(process.env.JUHE_PAGE_DATA_CONFIRM_BENCHMARK_ITERATIONS, 2_000)
const scenarios = [
  { name: '1-domain-sequential', domains: ['accounts.runtime'] as PageDataDomain[], concurrency: 1 },
  { name: '3-domain-sequential', domains: ['accounts.static', 'accounts.runtime', 'usage.records'] as PageDataDomain[], concurrency: 1 },
  { name: '4-domain-sequential', domains: ['accounts.static', 'accounts.runtime', 'usage.records', 'announcements.public'] as PageDataDomain[], concurrency: 1 },
  { name: '1-domain-concurrent-20', domains: ['accounts.runtime'] as PageDataDomain[], concurrency: 20 },
  { name: '3-domain-concurrent-20', domains: ['accounts.static', 'accounts.runtime', 'usage.records'] as PageDataDomain[], concurrency: 20 },
  { name: '4-domain-concurrent-20', domains: ['accounts.static', 'accounts.runtime', 'usage.records', 'announcements.public'] as PageDataDomain[], concurrency: 20 }
]

try {
  await runBenchmark(requiredRedisUrl())
} catch (error) {
  console.error(`真实 Redis 页面确认 benchmark 失败：${error instanceof Error ? error.message : String(error)}`)
  process.exitCode = 1
}

async function runBenchmark(redisUrl: string): Promise<void> {
  const keyPrefix = `benchmark:page-data-confirm:${randomUUID()}`
  const epochKey = `${keyPrefix}:epoch:v${PAGE_DATA_PROTOCOL_VERSION}`
  const rawClient = await createDedicatedRedisClient(redisUrl)
  const client = createCountingRedisClient(rawClient)
  const store = createRedisPageDataChangeStore({ client, keyPrefix, epoch: randomUUID() })
  const scope = pageDataScope({ viewerSystemAccountId: 'benchmark-user', viewScope: 'self' })
  try {
    const results: BenchmarkResult[] = []
    for (const scenario of scenarios) {
      results.push(await runScenario({ ...scenario, client, store, scope }))
    }
    console.log(JSON.stringify({ mode: 'real-redis', iterationsPerScenario: iterations, results }, null, 2))
  } finally {
    try {
      await rawClient.del(epochKey)
    } finally {
      await rawClient.quit?.().catch(() => undefined)
      try {
        rawClient.destroy?.()
      } catch {
        // node-redis throws when destroy() follows a clean quit().
      }
    }
  }
}

async function runScenario(input: {
  name: string
  domains: PageDataDomain[]
  concurrency: number
  client: CountingRedisClient
  store: ReturnType<typeof createRedisPageDataChangeStore>
  scope: ReturnType<typeof pageDataScope>
}): Promise<BenchmarkResult> {
  const initial = await input.store.confirm(input.scope, Object.fromEntries(input.domains.map((domain) => [domain, undefined])))
  const tokens = Object.fromEntries(input.domains.map((domain) => {
    const token = initial.domains[domain]?.token
    assert(token)
    return [domain, token]
  })) as Record<string, PageDataRevisionToken>
  for (let index = 0; index < 100; index += 1) await input.store.confirm(input.scope, tokens)

  input.client.resetCommandCounts()
  const latencies: number[] = []
  let nextIndex = 0
  await Promise.all(Array.from({ length: input.concurrency }, async () => {
    while (nextIndex < iterations) {
      nextIndex += 1
      const startedAt = performance.now()
      const result = await input.store.confirm(input.scope, tokens)
      latencies.push(performance.now() - startedAt)
      assert(input.domains.every((domain) => result.domains[domain]?.action === 'unchanged'))
    }
  }))

  const counts = input.client.commandCounts()
  const redisCommandsPerConfirm = {
    eval: counts.eval / iterations,
    get: counts.get / iterations,
    set: counts.set / iterations,
    sendCommand: counts.sendCommand / iterations
  }
  assert.deepEqual(redisCommandsPerConfirm, { eval: 1, get: 0, set: 0, sendCommand: 0 })
  latencies.sort((left, right) => left - right)
  return {
    name: input.name,
    domains: input.domains.length,
    concurrency: input.concurrency,
    confirms: iterations,
    p50Ms: percentile(latencies, 50),
    p95Ms: percentile(latencies, 95),
    p99Ms: percentile(latencies, 99),
    redisCommandsPerConfirm
  }
}

function createCountingRedisClient(client: RedisCommandClient): CountingRedisClient {
  const counts: CommandCounts = { eval: 0, get: 0, set: 0, sendCommand: 0 }
  return {
    commandCounts: () => ({ ...counts }),
    resetCommandCounts: () => { counts.eval = 0; counts.get = 0; counts.set = 0; counts.sendCommand = 0 },
    async get(key) { counts.get += 1; return client.get(key) },
    async set(key, value, options) { counts.set += 1; return client.set(key, value, options) },
    async eval(script, options) { counts.eval += 1; return client.eval(script, options) },
    async sendCommand(command) { counts.sendCommand += 1; return client.sendCommand(command) }
  }
}

function requiredRedisUrl(): string {
  const value = process.env[redisUrlEnvironmentName]?.trim()
  if (!value) throw new Error(`必须设置 ${redisUrlEnvironmentName}，真实 benchmark 不会 fallback 到 fake Redis`)
  return value
}

function percentile(sorted: number[], value: number): number {
  const index = Math.max(0, Math.ceil(sorted.length * value / 100) - 1)
  return Number((sorted[index] ?? 0).toFixed(4))
}

function positiveInteger(value: string | undefined, fallback: number): number {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : fallback
}
