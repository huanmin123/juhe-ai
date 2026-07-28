import { randomUUID } from 'node:crypto'
import { mkdir, open, opendir, readFile, rename, stat, unlink } from 'node:fs/promises'
import { basename, dirname, join } from 'node:path'

import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import type { UsageRecordInput } from '../../../storage/repositories.js'

interface UsageSpoolCapacity {
  items: number
  bytes: number
  refreshedAt: number
}

export interface UsageRecordSpoolRuntime {
  pendingItems: number
  pendingBytes: number
  persistedCount: number
  replayedCount: number
  persistFailureCount: number
  replayFailureCount: number
  lastPersistedAt?: string
  lastReplayedAt?: string
  lastError?: string
}

const capacityRefreshIntervalMs = 30_000
const temporaryFileRetentionMs = 60 * 60_000
let capacity: UsageSpoolCapacity | undefined
let capacityRefreshPromise: Promise<UsageSpoolCapacity> | undefined
let persistSequence = Promise.resolve()
let replayStarted = false
let replayStopping = false
let replayPromise: Promise<void> | undefined
let wakeReplayDelay: (() => void) | undefined
let replayDirectoryCursor = 0
const runtime: UsageRecordSpoolRuntime = {
  pendingItems: 0,
  pendingBytes: 0,
  persistedCount: 0,
  replayedCount: 0,
  persistFailureCount: 0,
  replayFailureCount: 0
}

export async function persistUsageRecordToSpool(input: UsageRecordInput): Promise<void> {
  if (runtimeConfig.runtimeMode !== 'performance') {
    throw new Error('usage spool 只能在 performance 模式使用')
  }
  const operation = persistSequence.then(() => persistUsageRecordToSpoolExclusive(input))
  persistSequence = operation.catch(() => undefined)
  await operation
}

export function startUsageRecordSpoolReplay(
  replay: (input: UsageRecordInput) => Promise<void>
): void {
  if (runtimeConfig.runtimeMode !== 'performance' || replayStarted) return
  replayStarted = true
  replayStopping = false
  replayPromise = runUsageRecordSpoolReplay(replay).catch((error) => {
    runtime.replayFailureCount += 1
    runtime.lastError = errorMessage(error)
    logger.error(errorLogFields(error, {
      event: 'usage_record_spool_replay_stopped'
    }), '使用记录持久补偿循环异常退出')
  }).finally(() => {
    replayStarted = false
    replayPromise = undefined
  })
}

export async function stopUsageRecordSpoolReplay(): Promise<void> {
  replayStopping = true
  wakeReplayDelay?.()
  await replayPromise?.catch(() => undefined)
}

export function getUsageRecordSpoolRuntime(): UsageRecordSpoolRuntime {
  return { ...runtime }
}

async function persistUsageRecordToSpoolExclusive(input: UsageRecordInput): Promise<void> {
  const encoded = Buffer.from(`${JSON.stringify(input)}\n`, 'utf8')
  try {
    const current = await currentCapacity(false)
    if (current.items + 1 > runtimeConfig.usageSpool.maxItems
      || current.bytes + encoded.byteLength > runtimeConfig.usageSpool.maxBytes) {
      const refreshed = await currentCapacity(true)
      if (refreshed.items + 1 > runtimeConfig.usageSpool.maxItems
        || refreshed.bytes + encoded.byteLength > runtimeConfig.usageSpool.maxBytes) {
        throw new Error(`usage spool 已达到容量上限：items=${refreshed.items}, bytes=${refreshed.bytes}`)
      }
    }

    const directory = instanceSpoolDirectory()
    await mkdir(directory, { recursive: true, mode: 0o700 })
    const token = `${Date.now()}-${process.pid}-${randomUUID()}`
    const temporaryPath = join(directory, `.${token}.tmp`)
    const finalPath = join(directory, `${token}.json`)
    const handle = await open(temporaryPath, 'wx', 0o600)
    try {
      await handle.writeFile(encoded)
      await handle.sync()
    } finally {
      await handle.close()
    }
    await rename(temporaryPath, finalPath)
    await syncDirectory(directory)
    const nextCapacity = capacity ?? { items: 0, bytes: 0, refreshedAt: Date.now() }
    nextCapacity.items += 1
    nextCapacity.bytes += encoded.byteLength
    capacity = nextCapacity
    runtime.pendingItems = nextCapacity.items
    runtime.pendingBytes = nextCapacity.bytes
    runtime.persistedCount += 1
    runtime.lastPersistedAt = new Date().toISOString()
  } catch (error) {
    runtime.persistFailureCount += 1
    runtime.lastError = errorMessage(error)
    throw error
  }
}

async function runUsageRecordSpoolReplay(replay: (input: UsageRecordInput) => Promise<void>): Promise<void> {
  while (!replayStopping) {
    const files = await listSpoolFiles(runtimeConfig.usageSpool.replayBatchSize)
    if (files.length === 0) {
      if (replayStopping) break
      await replayDelay(runtimeConfig.usageSpool.replayIntervalMs)
      continue
    }

    let shouldBackoff = false
    for (const filePath of files) {
      if (replayStopping) break
      let input: UsageRecordInput
      try {
        input = parseUsageRecord(await readFile(filePath, 'utf8'), filePath)
      } catch (error) {
        runtime.replayFailureCount += 1
        runtime.lastError = errorMessage(error)
        try {
          await quarantineCorruptSpoolFile(filePath)
          logger.error(errorLogFields(error, {
            event: 'usage_record_spool_file_quarantined',
            spoolFile: basename(filePath)
          }), '使用记录持久补偿文件损坏，已隔离并继续重放后续记录')
          capacity = undefined
          continue
        } catch (quarantineError) {
          logger.error(errorLogFields(quarantineError, {
            event: 'usage_record_spool_file_quarantine_failed',
            spoolFile: basename(filePath)
          }), '使用记录持久补偿损坏文件隔离失败')
          shouldBackoff = true
          break
        }
      }
      try {
        await replay(input)
        const fileStats = await stat(filePath).catch(() => undefined)
        await unlink(filePath).catch((error) => {
          if (!isMissingFileError(error)) throw error
        })
        if (fileStats) {
          runtime.pendingItems = Math.max(0, runtime.pendingItems - 1)
          runtime.pendingBytes = Math.max(0, runtime.pendingBytes - fileStats.size)
        }
        runtime.replayedCount += 1
        runtime.lastReplayedAt = new Date().toISOString()
      } catch (error) {
        runtime.replayFailureCount += 1
        runtime.lastError = errorMessage(error)
        logger.warn(errorLogFields(error, {
          event: 'usage_record_spool_replay_failed',
          spoolFile: basename(filePath),
          replayFailureCount: runtime.replayFailureCount
        }), '使用记录持久补偿重放失败，文件保留等待下一轮')
        shouldBackoff = true
        break
      }
    }
    capacity = undefined
    if (shouldBackoff) {
      if (replayStopping) break
      await replayDelay(runtimeConfig.usageSpool.replayIntervalMs)
    } else {
      await yieldImmediate()
    }
  }
}

async function currentCapacity(force: boolean): Promise<UsageSpoolCapacity> {
  if (!force && capacity && Date.now() - capacity.refreshedAt < capacityRefreshIntervalMs) {
    return capacity
  }
  if (!capacityRefreshPromise) {
    capacityRefreshPromise = scanSpoolCapacity().finally(() => {
      capacityRefreshPromise = undefined
    })
  }
  capacity = await capacityRefreshPromise
  runtime.pendingItems = capacity.items
  runtime.pendingBytes = capacity.bytes
  return capacity
}

async function scanSpoolCapacity(): Promise<UsageSpoolCapacity> {
  let items = 0
  let bytes = 0
  for await (const filePath of walkSpoolCapacityFiles()) {
    const fileStats = await stat(filePath).catch(() => undefined)
    if (!fileStats?.isFile()) continue
    if (filePath.endsWith('.tmp') && Date.now() - fileStats.mtimeMs >= temporaryFileRetentionMs) {
      await unlink(filePath).catch((error) => {
        if (!isMissingFileError(error)) throw error
      })
      continue
    }
    items += 1
    bytes += fileStats.size
  }
  return { items, bytes, refreshedAt: Date.now() }
}

async function listSpoolFiles(limit: number): Promise<string[]> {
  const directories = await listInstanceSpoolDirectories()
  if (directories.length === 0) return []
  const files: string[] = []
  const startIndex = replayDirectoryCursor % directories.length
  replayDirectoryCursor = (startIndex + 1) % directories.length
  for (let offset = 0; offset < directories.length && files.length < limit; offset += 1) {
    const directoryPath = directories[(startIndex + offset) % directories.length]
    if (!directoryPath) continue
    const directory = await opendir(directoryPath).catch((error) => {
      if (isMissingFileError(error)) return undefined
      throw error
    })
    if (!directory) continue
    for await (const file of directory) {
      if (!file.isFile() || !file.name.endsWith('.json')) continue
      files.push(join(directoryPath, file.name))
      if (files.length >= limit) break
    }
  }
  files.sort()
  return files
}

async function listInstanceSpoolDirectories(): Promise<string[]> {
  let root
  try {
    root = await opendir(runtimeConfig.usageSpool.directory)
  } catch (error) {
    if (isMissingFileError(error)) return []
    throw error
  }
  const directories: string[] = []
  for await (const entry of root) {
    if (entry.isDirectory()) directories.push(join(runtimeConfig.usageSpool.directory, entry.name))
  }
  directories.sort()
  return directories
}

async function* walkSpoolCapacityFiles(): AsyncGenerator<string> {
  let root
  try {
    root = await opendir(runtimeConfig.usageSpool.directory)
  } catch (error) {
    if (isMissingFileError(error)) return
    throw error
  }
  for await (const entry of root) {
    if (!entry.isDirectory()) continue
    const directoryPath = join(runtimeConfig.usageSpool.directory, entry.name)
    const directory = await opendir(directoryPath).catch((error) => {
      if (isMissingFileError(error)) return undefined
      throw error
    })
    if (!directory) continue
    for await (const file of directory) {
      if (file.isFile() && (file.name.endsWith('.json') || file.name.endsWith('.tmp') || file.name.endsWith('.corrupt'))) {
        yield join(directoryPath, file.name)
      }
    }
  }
}

async function quarantineCorruptSpoolFile(filePath: string): Promise<void> {
  await rename(filePath, `${filePath}.corrupt`)
  await syncDirectory(dirname(filePath))
}

async function syncDirectory(directory: string): Promise<void> {
  if (process.platform === 'win32') return
  const handle = await open(directory, 'r')
  try {
    await handle.sync()
  } finally {
    await handle.close()
  }
}

function instanceSpoolDirectory(): string {
  return join(runtimeConfig.usageSpool.directory, runtimeConfig.instanceId)
}

function parseUsageRecord(text: string, filePath: string): UsageRecordInput {
  const parsed = JSON.parse(text) as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`usage spool 文件格式错误：${basename(filePath)}`)
  }
  const id = (parsed as { id?: unknown }).id
  const createdAt = (parsed as { createdAt?: unknown }).createdAt
  if (typeof id !== 'string' || !id || typeof createdAt !== 'string' || !createdAt) {
    throw new Error(`usage spool 文件缺少稳定 id/createdAt：${basename(filePath)}`)
  }
  return parsed as UsageRecordInput
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object'
    && error !== null
    && (error as { code?: unknown }).code === 'ENOENT'
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function replayDelay(ms: number): Promise<void> {
  if (replayStopping) return Promise.resolve()
  return new Promise((resolve) => {
    let settled = false
    const finish = () => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      if (wakeReplayDelay === finish) wakeReplayDelay = undefined
      resolve()
    }
    const timer = setTimeout(finish, ms)
    timer.unref()
    wakeReplayDelay = finish
  })
}

function yieldImmediate(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}
