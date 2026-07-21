import { createWriteStream, type Stats, type WriteStream } from 'node:fs'
import { mkdir, opendir, rename, stat, unlink } from 'node:fs/promises'
import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { parentPort, workerData } from 'node:worker_threads'

import type { LogWriterWorkerOptions } from './log-writer-worker-client.js'
import type { LogWriterWorkerLineOptions } from './log-writer-worker-client.js'
import type { LogWriterWorkerParsedMetadata } from './log-writer-worker-client.js'

type WorkerMessage =
  | { type: 'write'; id: number; chunk: Uint8Array }
  | { type: 'write_batch'; id: number; chunks: Uint8Array[] }
  | { type: 'close' }

if (!parentPort) throw new Error('日志 writer worker 缺少 parentPort')
const port = parentPort
const options = workerData as LogWriterWorkerOptions
const currentPath = join(options.directory, options.fileName)
let currentSize = 0
let currentFileIdentity: string | undefined
let fileStream: WriteStream | undefined
let chain = initialize()

port.on('message', (message: WorkerMessage) => {
  if (message.type === 'close') {
    chain.then(async () => {
      await closeFileStream()
      port.postMessage({ type: 'closed' })
      port.close()
    }).catch(reportError)
    return
  }
  chain = chain.then(async () => {
    const chunks = message.type === 'write'
      ? [rehydrateBuffer(message.chunk)]
      : message.chunks.map(rehydrateBuffer)
    const writeResult = await writeChunk(chunks.length === 1 ? chunks[0] : Buffer.concat(chunks))
    let nextLogOffset = writeResult.logOffset
    const lineOptionsList: LogWriterWorkerLineOptions[] | undefined = options.fileEnabled
      ? chunks.map((chunk) => {
        const lineOptions = {
          logFile: currentPath,
          logFileIdentity: writeResult.logFileIdentity,
          logOffset: nextLogOffset,
          parsedMetadata: parseLogIndexMetadata(chunk)
        }
        nextLogOffset += chunk.byteLength
        return lineOptions
      })
      : undefined
    port.postMessage(chunks.length === 1
      ? { type: 'line', chunk: chunks[0], lineOptions: lineOptionsList?.[0] }
      : { type: 'line', chunks, lineOptionsList })
    port.postMessage({ type: 'ack', id: message.id })
  }).catch((error) => {
    port.postMessage({ type: 'ack', id: message.id, error: error instanceof Error ? error.message : String(error) })
  })
})

function rehydrateBuffer(chunk: Uint8Array): Buffer {
  return Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength)
}

function parseLogIndexMetadata(chunk: Buffer): LogWriterWorkerParsedMetadata | undefined {
  if (chunk.byteLength > 128 * 1024) return undefined
  try {
    const value = JSON.parse(chunk.toString('utf8')) as unknown
    if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
    const record = value as Record<string, unknown>
    const err = record.err && typeof record.err === 'object' && !Array.isArray(record.err)
      ? record.err as Record<string, unknown>
      : undefined
    return {
      time: stringField(record.time),
      level: typeof record.level === 'number' || typeof record.level === 'string' ? record.level : undefined,
      traceId: stringField(record.traceId),
      event: stringField(record.event),
      message: stringField(record.msg) ?? stringField(record.message),
      errorMessage: stringField(record.errorMessage) ?? stringField(err?.message)
    }
  } catch {
    return undefined
  }
}

function stringField(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

async function initialize(): Promise<void> {
  await mkdir(options.directory, { recursive: true })
  if (!options.fileEnabled) return
  try {
    const stats = await stat(currentPath)
    currentSize = stats.size
    currentFileIdentity = runtimeLogFileIdentity(stats)
  } catch {
    currentSize = 0
  }
  fileStream = openFileStream()
  await cleanupRotatedFiles()
}

async function writeChunk(chunk: Buffer): Promise<{ logOffset: number; logFileIdentity?: string }> {
  let logOffset = 0
  if (options.fileEnabled) {
    if (currentSize > 0 && currentSize + chunk.byteLength > options.maxFileBytes) await rotate()
    logOffset = currentSize
    await writeFileChunk(chunk)
    currentSize += chunk.byteLength
    currentFileIdentity ??= runtimeLogFileIdentity(await stat(currentPath))
  }
  if (options.consoleEnabled) {
    await new Promise<void>((resolve, reject) => {
      process.stdout.write(chunk, (error) => error ? reject(error) : resolve())
    })
  }
  return { logOffset, logFileIdentity: currentFileIdentity }
}

async function rotate(): Promise<void> {
  await closeFileStream()
  const timestamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
  const rotatedPath = join(options.directory, `juhe-ai.${timestamp}.${randomUUID()}.log`)
  try {
    await rename(currentPath, rotatedPath)
  } catch (error) {
    const code = error && typeof error === 'object' && 'code' in error ? error.code : undefined
    if (code !== 'ENOENT') throw error
  }
  currentSize = 0
  currentFileIdentity = undefined
  fileStream = openFileStream()
  await cleanupRotatedFiles()
}

function runtimeLogFileIdentity(stats: Stats): string {
  const role = options.fileName === 'juhe-ai.worker.log'
    ? 'worker-current'
    : options.fileName === 'juhe-ai.db-service.log'
      ? 'db-service-current'
      : 'server-current'
  return [role, stats.dev, stats.ino, Math.trunc(stats.birthtimeMs)].join(':')
}

function openFileStream(): WriteStream {
  const stream = createWriteStream(currentPath, { flags: 'a' })
  stream.on('error', () => undefined)
  return stream
}

async function writeFileChunk(chunk: Buffer): Promise<void> {
  const stream = fileStream ?? (fileStream = openFileStream())
  await new Promise<void>((resolve, reject) => {
    stream.write(chunk, (error) => error ? reject(error) : resolve())
  })
}

async function closeFileStream(): Promise<void> {
  const stream = fileStream
  fileStream = undefined
  if (!stream) return
  await new Promise<void>((resolve, reject) => {
    let settled = false
    const settle = (error?: Error) => {
      if (settled) return
      settled = true
      stream.off('error', onError)
      stream.off('close', onClose)
      error ? reject(error) : resolve()
    }
    const onError = (error: Error) => settle(error)
    const onClose = () => settle()
    stream.once('error', onError)
    stream.once('close', onClose)
    stream.end()
  })
}

async function cleanupRotatedFiles(): Promise<void> {
  const expiresBefore = Date.now() - options.retentionDays * 24 * 60 * 60 * 1000
  const maxRotated = Math.max(0, options.maxFiles - 1)
  const retained: Array<{ path: string; mtimeMs: number }> = []
  try {
    const directory = await opendir(options.directory)
    for await (const entry of directory) {
      if (!entry.isFile() || !isRotatedLogFileName(entry.name)) continue
      const path = join(options.directory, entry.name)
      const file = await stat(path).catch(() => undefined)
      if (!file) continue
      if (file.mtimeMs < expiresBefore || maxRotated === 0) {
        await unlink(path).catch(() => undefined)
        continue
      }
      retained.push({ path, mtimeMs: file.mtimeMs })
      retained.sort((left, right) => right.mtimeMs - left.mtimeMs)
      if (retained.length > maxRotated) {
        const removed = retained.pop()
        if (removed) await unlink(removed.path).catch(() => undefined)
      }
    }
  } catch {
    return
  }
}

function isRotatedLogFileName(fileName: string): boolean {
  return /^juhe-ai\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$/i.test(fileName)
}

function reportError(error: unknown): void {
  port.postMessage({ type: 'error', error: error instanceof Error ? error.message : String(error) })
}
