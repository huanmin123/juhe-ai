import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'
import process from 'node:process'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import * as databaseModule from '../../storage/database.js'
import * as writerPool from '../../storage/codex-context-state-writer-pool.js'

interface PressureConfig {
  shardCounts: number[]
  writerPoolModes: boolean[]
  sessions: number
  responsesPerSession: number
  sameSessionResponses: number
  compactCount: number
  expiredSessions: number
  expiredResponsesPerSession: number
}

interface TimedReport {
  name: string
  operations: number
  durationMs: number
  opsPerSecond: number
  latency: {
    p50Ms: number
    p95Ms: number
    p99Ms: number
    maxMs: number
  }
  extra?: Record<string, unknown>
}

const pressureConfig: PressureConfig = {
  shardCounts: intListEnv('JUHE_CODEX_CONTEXT_PRESSURE_SHARDS', [1, 16], 1, 256),
  writerPoolModes: boolListEnv('JUHE_CODEX_CONTEXT_PRESSURE_WRITER_POOL', [false, true]),
  sessions: intEnv('JUHE_CODEX_CONTEXT_PRESSURE_SESSIONS', 32, 1, 10000),
  responsesPerSession: intEnv('JUHE_CODEX_CONTEXT_PRESSURE_RESPONSES_PER_SESSION', 16, 1, 1000),
  sameSessionResponses: intEnv('JUHE_CODEX_CONTEXT_PRESSURE_SAME_SESSION_RESPONSES', 256, 1, 100000),
  compactCount: intEnv('JUHE_CODEX_CONTEXT_PRESSURE_COMPACTS', 256, 1, 100000),
  expiredSessions: intEnv('JUHE_CODEX_CONTEXT_PRESSURE_EXPIRED_SESSIONS', 16, 1, 10000),
  expiredResponsesPerSession: intEnv('JUHE_CODEX_CONTEXT_PRESSURE_EXPIRED_RESPONSES_PER_SESSION', 16, 1, 1000)
}

const baseTempRoot = resolve(tmpdir(), `juhe-ai-codex-context-pressure-${Date.now()}-${Math.random().toString(16).slice(2)}`)
logger.level = 'silent'

const boundary = {
  systemAccountId: 'sys_pressure',
  apiKeyId: 'apikey_pressure',
  groupId: 'group_pressure',
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}

const futureExpiresAt = '2999-01-01T00:00:00.000Z'
const expiredAt = '2000-01-01T00:00:00.000Z'
const cleanupNow = '2026-06-22T00:00:00.000Z'

try {
  const reports = []
  for (const shardCount of pressureConfig.shardCounts) {
    for (const writerPoolEnabled of pressureConfig.writerPoolModes) {
      reports.push(await runShardScenario(shardCount, writerPoolEnabled))
    }
  }
  console.log(JSON.stringify({
    config: pressureConfig,
    reports
  }, null, 2))
} finally {
  await writerPool.closeCodexContextStateWriterPool()
  databaseModule.closeStorageDatabases()
  rmSync(baseTempRoot, { recursive: true, force: true })
}

async function runShardScenario(shardCount: number, writerPoolEnabled: boolean): Promise<Record<string, unknown>> {
  await writerPool.closeCodexContextStateWriterPool()
  databaseModule.closeStorageDatabases()
  const scenarioRoot = join(baseTempRoot, `shards-${shardCount}-${writerPoolEnabled ? 'pool' : 'direct'}`)
  mkdirSync(scenarioRoot, { recursive: true })
  runtimeConfig.databasePath = join(scenarioRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(scenarioRoot, 'dataset.sqlite3')
  runtimeConfig.usageCatalogDatabasePath = join(scenarioRoot, 'usage-catalog.sqlite3')
  runtimeConfig.statsDatabasePath = join(scenarioRoot, 'stats.sqlite3')
  runtimeConfig.usageShardRoot = join(scenarioRoot, 'usage-shards')
  runtimeConfig.codexContextRoot = join(scenarioRoot, 'codex-context')
  runtimeConfig.codexContextStateShardRoot = join(scenarioRoot, 'codex-context', 'state-shards')
  runtimeConfig.codexContextStateShardCount = shardCount
  runtimeConfig.codexContextStateWriterPoolEnabled = writerPoolEnabled
  runtimeConfig.secret = 'codex-context-pressure-secret'
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  runtimeConfig.processRole = 'db-service'

  const warmupStartedAt = performance.now()
  warmupShards()
  const warmupMs = performance.now() - warmupStartedAt
  const scenarioStartedAt = performance.now()
  const cpuStartedAt = process.cpuUsage()
  const tails: string[] = []
  const reports: TimedReport[] = []

  reports.push(await measureOperations('cross_session_response_writes', pressureConfig.sessions * pressureConfig.responsesPerSession, async (record) => {
    await Promise.all(Array.from({ length: pressureConfig.sessions }, async (_, sessionIndex) => {
      const sessionId = `cross_session_${sessionIndex}`
      let previousResponseId: string | undefined
      for (let responseIndex = 0; responseIndex < pressureConfig.responsesPerSession; responseIndex += 1) {
        const responseId = `resp_cross_${sessionIndex}_${responseIndex}`
        await record(() => saveResponse({
          responseId,
          sessionId,
          previousResponseId,
          responseIndex,
          expiresAt: futureExpiresAt
        }))
        previousResponseId = responseId
      }
      if (previousResponseId) tails.push(previousResponseId)
    }))
  }))

  reports.push(await measureOperations('same_session_response_writes', pressureConfig.sameSessionResponses, async (record) => {
    const sessionId = 'same_session_pressure'
    let previousResponseId: string | undefined
    for (let index = 0; index < pressureConfig.sameSessionResponses; index += 1) {
      const responseId = `resp_same_${index}`
      await record(() => saveResponse({
        responseId,
        sessionId,
        previousResponseId,
        responseIndex: index,
        expiresAt: futureExpiresAt
      }))
      previousResponseId = responseId
    }
    if (previousResponseId) tails.push(previousResponseId)
  }))

  reports.push(await measureOperations('compact_state_writes', pressureConfig.compactCount, async (record) => {
    await Promise.all(Array.from({ length: pressureConfig.compactCount }, async (_, index) => {
      await record(async () => {
        await writerPool.saveCodexContextCompactStateIndexWithWriterPool({
          compactId: `cmp_pressure_${index}`,
          sessionId: `compact_session_${index % Math.max(1, pressureConfig.sessions)}`,
          sourceResponseId: tails[index % Math.max(1, tails.length)],
          summaryDigest: digestLike('d', index),
          ...boundary,
          storageKey: `sessions/compact_${index % 64}/segments/2026062200.json.gz`,
          storageOffsetBytes: index * 96,
          sha256: digestLike('e', index),
          rawSizeBytes: 120,
          compressedSizeBytes: 80,
          compression: 'gzip',
          schemaVersion: 2,
          expiresAt: futureExpiresAt
        })
      })
    }))
  }))

  reports.push(await measureOperations('response_chain_restores', tails.length, async (record) => {
    await Promise.all(tails.map(async (responseId) => {
      await record(async () => {
        const result = await writerPool.readCodexContextResponseStateChainWithWriterPool({
          responseId,
          boundary,
          maxDepth: Math.max(pressureConfig.responsesPerSession, pressureConfig.sameSessionResponses) + 1,
          now: cleanupNow,
          refreshExpiresAt: futureExpiresAt
        })
        assert.equal(result.outcome, 'found')
      })
    }))
  }))

  const expiredRows = pressureConfig.expiredSessions * pressureConfig.expiredResponsesPerSession
  reports.push(await measureOperations('expired_response_writes', expiredRows, async (record) => {
    await Promise.all(Array.from({ length: pressureConfig.expiredSessions }, async (_, sessionIndex) => {
      const sessionId = `expired_session_${sessionIndex}`
      let previousResponseId: string | undefined
      for (let responseIndex = 0; responseIndex < pressureConfig.expiredResponsesPerSession; responseIndex += 1) {
        const responseId = `resp_expired_${sessionIndex}_${responseIndex}`
        await record(() => saveResponse({
          responseId,
          sessionId,
          previousResponseId,
          responseIndex,
          expiresAt: expiredAt
        }))
        previousResponseId = responseId
      }
    }))
  }))

  reports.push(await measureOperations('cleanup_expired_states', 1, async (record) => {
    await record(async () => {
      const result = await writerPool.cleanupExpiredCodexContextStatesWithWriterPool({
        expiredBefore: cleanupNow,
        limit: 100000
      })
      assert.equal(result.deletedResponses, expiredRows)
      assert.equal(result.deletedSessions, pressureConfig.expiredSessions)
    })
  }, {
    expiredRows
  }))

  const cpu = process.cpuUsage(cpuStartedAt)
  const totalDurationMs = performance.now() - scenarioStartedAt
  const shardCounts = countRowsByShard('codex_context_responses')
  const totalRows = shardCounts.reduce((sum, count) => sum + count, 0)
  return {
    shardCount,
    writerPoolEnabled,
    writerPoolRuntime: writerPool.getCodexContextStateWriterPoolRuntime(),
    warmupMs: round(warmupMs),
    totalDurationMs: round(totalDurationMs),
    totalOps: reports.reduce((sum, report) => sum + report.operations, 0),
    overallOpsPerSecond: round(reports.reduce((sum, report) => sum + report.operations, 0) / (totalDurationMs / 1000)),
    cpuUserMs: round(cpu.user / 1000),
    cpuSystemMs: round(cpu.system / 1000),
    shardResponseRows: shardCounts,
    nonEmptyResponseShards: shardCounts.filter((count) => count > 0).length,
    responseRowsAfterCleanup: totalRows,
    sqliteBytes: sqliteShardBytes(),
    scenarios: reports
  }
}

function warmupShards(): void {
  for (const shardIndex of databaseModule.codexContextStateShardIndexes()) {
    databaseModule.getCodexContextStateShardDatabase(shardIndex)
  }
}

async function saveResponse(input: {
  responseId: string
  sessionId: string
  previousResponseId?: string
  responseIndex: number
  expiresAt: string
}): Promise<void> {
  await writerPool.saveCodexContextResponseStateIndexWithWriterPool({
    responseId: input.responseId,
    sessionId: input.sessionId,
    previousResponseId: input.previousResponseId,
    ...boundary,
    upstreamAccountId: `acct_${input.responseIndex % 7}`,
    model: 'deepseek-v4-flash',
    upstreamModel: 'deepseek-v4-flash',
    storageKey: `sessions/${input.sessionId}/segments/2026062200.json.gz`,
    storageOffsetBytes: input.responseIndex * 128,
    sha256: digestLike('a', input.responseIndex),
    rawSizeBytes: 100 + input.responseIndex,
    compressedSizeBytes: 80 + input.responseIndex,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: input.expiresAt
  })
}

async function measureOperations(
  name: string,
  operations: number,
  run: (record: (operation: () => void | Promise<void>) => Promise<void>) => void | Promise<void>,
  extra?: Record<string, unknown>
): Promise<TimedReport> {
  const samples: number[] = []
  const startedAt = performance.now()
  await run(async (operation) => {
    const operationStartedAt = performance.now()
    await operation()
    samples.push(performance.now() - operationStartedAt)
  })
  const durationMs = performance.now() - startedAt
  return {
    name,
    operations,
    durationMs: round(durationMs),
    opsPerSecond: round(operations / (durationMs / 1000)),
    latency: percentiles(samples),
    extra
  }
}

function percentiles(samples: number[]): TimedReport['latency'] {
  if (samples.length === 0) {
    return {
      p50Ms: 0,
      p95Ms: 0,
      p99Ms: 0,
      maxMs: 0
    }
  }
  const ordered = [...samples].sort((a, b) => a - b)
  return {
    p50Ms: round(percentile(ordered, 0.50)),
    p95Ms: round(percentile(ordered, 0.95)),
    p99Ms: round(percentile(ordered, 0.99)),
    maxMs: round(ordered[ordered.length - 1] ?? 0)
  }
}

function percentile(ordered: number[], p: number): number {
  const index = Math.min(ordered.length - 1, Math.max(0, Math.ceil(ordered.length * p) - 1))
  return ordered[index] ?? 0
}

function countRowsByShard(table: string): number[] {
  return databaseModule.codexContextStateShardIndexes().map((shardIndex) => {
    const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
    const row = database.prepare(`SELECT COUNT(*) AS count FROM ${table}`).get() as { count?: number | bigint } | undefined
    return Number(row?.count ?? 0)
  })
}

function sqliteShardBytes(): number {
  let total = 0
  for (const shardIndex of databaseModule.codexContextStateShardIndexes()) {
    for (const suffix of ['', '-wal', '-shm']) {
      const filePath = databaseModule.codexContextStateShardPath(shardIndex) + suffix
      if (existsSync(filePath)) {
        total += statSync(filePath).size
      }
    }
  }
  return total
}

function digestLike(seed: string, index: number): string {
  return `${seed}${index.toString(16).padStart(8, '0')}${seed.repeat(64)}`.slice(0, 64)
}

function intEnv(name: string, fallback: number, min: number, max: number): number {
  const raw = process.env[name]
  if (!raw) return fallback
  const parsed = Number.parseInt(raw, 10)
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, parsed))
}

function intListEnv(name: string, fallback: number[], min: number, max: number): number[] {
  const raw = process.env[name]
  if (!raw) return fallback
  const values = raw.split(',')
    .map((value) => Number.parseInt(value.trim(), 10))
    .filter((value) => Number.isFinite(value))
    .map((value) => Math.max(min, Math.min(max, value)))
  return values.length > 0 ? [...new Set(values)] : fallback
}

function boolListEnv(name: string, fallback: boolean[]): boolean[] {
  const raw = process.env[name]
  if (!raw) return fallback
  const values = raw.split(',')
    .map((value) => value.trim().toLowerCase())
    .map((value) => {
      if (['1', 'true', 'yes', 'on', 'pool'].includes(value)) return true
      if (['0', 'false', 'no', 'off', 'direct'].includes(value)) return false
      return undefined
    })
    .filter((value): value is boolean => typeof value === 'boolean')
  return values.length > 0 ? [...new Set(values)] : fallback
}

function round(value: number): number {
  return Math.round(value * 100) / 100
}
