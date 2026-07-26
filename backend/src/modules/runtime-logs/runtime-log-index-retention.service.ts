import { setImmediate as yieldToEventLoop, setTimeout as sleep } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'
import {
  cleanupRuntimeLogFileCursorsBeforeAsync,
  cleanupRuntimeLogIndexAsync
} from '../../storage/runtime-logs.repository.js'
import { DATA_RETENTION_CLEANUP_BATCH_PAUSE_MS } from '../background/data-retention-cleanup.constants.js'

export interface RuntimeLogIndexRetentionResult {
  runtimeLogs: number
  runtimeLogFileCursors: number
}

export interface RuntimeLogIndexRetentionDependencies {
  cleanupRuntimeLogs?: (cutoffIso: string, limit: number) => Promise<number>
  cleanupRuntimeLogFileCursors?: (cutoffIso: string, limit: number) => Promise<number>
  pauseBetweenBatches?: () => Promise<void>
}

export async function cleanupRuntimeLogIndexRetention(
  input: { cutoffIso: string; batchSize: number; maxBatches: number; signal?: AbortSignal },
  dependencies: RuntimeLogIndexRetentionDependencies = {}
): Promise<RuntimeLogIndexRetentionResult> {
  if (!runtimeConfig.log.indexEnabled) {
    return { runtimeLogs: 0, runtimeLogFileCursors: 0 }
  }

  const cleanupRuntimeLogs = dependencies.cleanupRuntimeLogs ?? cleanupRuntimeLogIndexAsync
  const cleanupRuntimeLogFileCursors = dependencies.cleanupRuntimeLogFileCursors ?? cleanupRuntimeLogFileCursorsBeforeAsync
  const pauseBetweenBatches = dependencies.pauseBetweenBatches
    ?? (() => sleep(DATA_RETENTION_CLEANUP_BATCH_PAUSE_MS, undefined, input.signal ? { signal: input.signal } : undefined))

  return {
    runtimeLogs: await cleanupInBatches(
      () => cleanupRuntimeLogs(input.cutoffIso, input.batchSize),
      input.batchSize,
      input.maxBatches,
      pauseBetweenBatches,
      input.signal
    ),
    runtimeLogFileCursors: await cleanupInBatches(
      () => cleanupRuntimeLogFileCursors(input.cutoffIso, input.batchSize),
      input.batchSize,
      input.maxBatches,
      pauseBetweenBatches,
      input.signal
    )
  }
}

async function cleanupInBatches(
  cleanupBatch: () => Promise<number>,
  batchSize: number,
  maxBatches: number,
  pauseBetweenBatches: () => Promise<void>,
  signal?: AbortSignal
): Promise<number> {
  let total = 0
  const normalizedBatchSize = Math.max(1, Math.trunc(batchSize))
  const normalizedMaxBatches = Math.max(1, Math.trunc(maxBatches))
  for (let index = 0; index < normalizedMaxBatches; index += 1) {
    signal?.throwIfAborted()
    const deleted = await cleanupBatch()
    total += deleted
    await yieldToEventLoop()
    signal?.throwIfAborted()
    if (deleted < normalizedBatchSize) break
    if (index < normalizedMaxBatches - 1) await pauseBetweenBatches()
  }
  return total
}
