import { appendFile, mkdir, opendir, rename, stat, unlink } from 'node:fs/promises'
import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { parentPort, workerData } from 'node:worker_threads'

import type { LogWriterWorkerOptions } from './log-writer-worker-client.js'

type WorkerMessage = { type: 'write'; id: number; chunk: Uint8Array } | { type: 'close' }

if (!parentPort) throw new Error('日志 writer worker 缺少 parentPort')
const port = parentPort
const options = workerData as LogWriterWorkerOptions
const currentPath = join(options.directory, options.fileName)
let currentSize = 0
let chain = initialize()

port.on('message', (message: WorkerMessage) => {
  if (message.type === 'close') {
    chain.then(() => {
      port.postMessage({ type: 'closed' })
      port.close()
    }).catch(reportError)
    return
  }
  chain = chain.then(async () => {
    const chunk = Buffer.from(message.chunk.buffer, message.chunk.byteOffset, message.chunk.byteLength)
    await writeChunk(chunk)
    port.postMessage({ type: 'line', chunk })
    port.postMessage({ type: 'ack', id: message.id })
  }).catch((error) => {
    port.postMessage({ type: 'ack', id: message.id, error: error instanceof Error ? error.message : String(error) })
  })
})

async function initialize(): Promise<void> {
  await mkdir(options.directory, { recursive: true })
  if (!options.fileEnabled) return
  try {
    currentSize = (await stat(currentPath)).size
  } catch {
    currentSize = 0
  }
  await cleanupRotatedFiles()
}

async function writeChunk(chunk: Buffer): Promise<void> {
  if (options.fileEnabled) {
    if (currentSize > 0 && currentSize + chunk.byteLength > options.maxFileBytes) await rotate()
    await appendFile(currentPath, chunk)
    currentSize += chunk.byteLength
  }
  if (options.consoleEnabled) {
    await new Promise<void>((resolve, reject) => {
      process.stdout.write(chunk, (error) => error ? reject(error) : resolve())
    })
  }
}

async function rotate(): Promise<void> {
  const timestamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
  const rotatedPath = join(options.directory, `juhe-ai.${timestamp}.${randomUUID()}.log`)
  try {
    await rename(currentPath, rotatedPath)
  } catch (error) {
    const code = error && typeof error === 'object' && 'code' in error ? error.code : undefined
    if (code !== 'ENOENT') throw error
  }
  currentSize = 0
  await cleanupRotatedFiles()
}

async function cleanupRotatedFiles(): Promise<void> {
  const expiresBefore = Date.now() - options.retentionDays * 24 * 60 * 60 * 1000
  const entries: Array<{ path: string; mtimeMs: number }> = []
  try {
    const directory = await opendir(options.directory)
    for await (const entry of directory) {
      if (!entry.isFile() || !isRotatedLogFileName(entry.name)) continue
      const path = join(options.directory, entry.name)
      const file = await stat(path).catch(() => undefined)
      if (file) entries.push({ path, mtimeMs: file.mtimeMs })
    }
  } catch {
    return
  }
  entries.sort((left, right) => right.mtimeMs - left.mtimeMs)
  const maxRotated = Math.max(0, options.maxFiles - 1)
  await Promise.all(entries.map(async (entry, index) => {
    if (index < maxRotated && entry.mtimeMs >= expiresBefore) return
    await unlink(entry.path).catch(() => undefined)
  }))
}

function isRotatedLogFileName(fileName: string): boolean {
  return /^juhe-ai\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$/i.test(fileName)
}

function reportError(error: unknown): void {
  port.postMessage({ type: 'error', error: error instanceof Error ? error.message : String(error) })
}
