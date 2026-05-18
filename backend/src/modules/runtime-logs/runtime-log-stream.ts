import { Writable } from 'node:stream'

export interface RuntimeLogLineSinkOptions {
  sourceKey?: string
  logFile?: string
  logOffset?: number
  lineNumber?: number
}

type RuntimeLogLineSink = (line: string, options?: RuntimeLogLineSinkOptions) => void
type PendingRuntimeLogLine = { line: string; options?: RuntimeLogLineSinkOptions }

let runtimeLogLineSink: RuntimeLogLineSink | undefined
const pendingRuntimeLogLines: PendingRuntimeLogLine[] = []
let pendingRuntimeLogLineBytes = 0

const maxPendingLineBytes = 256 * 1024
const maxPendingQueueLines = 1000
const maxPendingQueueBytes = 1024 * 1024

export function setRuntimeLogLineSink(sink?: RuntimeLogLineSink): void {
  runtimeLogLineSink = sink
  if (!runtimeLogLineSink) {
    return
  }

  const lines = pendingRuntimeLogLines.splice(0, pendingRuntimeLogLines.length)
  pendingRuntimeLogLineBytes = 0
  for (const item of lines) {
    runtimeLogLineSink(item.line, item.options)
  }
}

export class RuntimeLogIndexStream extends Writable {
  private pending = ''
  private lineSequence = 0

  _write(chunk: Buffer | string, encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    try {
      const text = Buffer.isBuffer(chunk) ? chunk.toString('utf8') : Buffer.from(chunk, encoding).toString('utf8')
      this.pending += text
      this.truncateOversizedPendingLine()
      this.drainLines(false)
      callback()
    } catch (error) {
      callback(error instanceof Error ? error : new Error(String(error)))
    }
  }

  _final(callback: (error?: Error | null) => void): void {
    try {
      this.drainLines(true)
      callback()
    } catch (error) {
      callback(error instanceof Error ? error : new Error(String(error)))
    }
  }

  private drainLines(includePartial: boolean): void {
    let newlineIndex = this.pending.indexOf('\n')
    while (newlineIndex >= 0) {
      const line = this.pending.slice(0, newlineIndex)
      this.pending = this.pending.slice(newlineIndex + 1)
      this.emitIndexedLine(line)
      newlineIndex = this.pending.indexOf('\n')
    }

    if (includePartial && this.pending.trim()) {
      this.emitIndexedLine(this.pending)
      this.pending = ''
    }
  }

  private truncateOversizedPendingLine(): void {
    if (Buffer.byteLength(this.pending, 'utf8') <= maxPendingLineBytes) {
      return
    }
    this.emitIndexedLine(`${this.pending.slice(0, maxPendingLineBytes)} [truncated: runtime log line exceeded pending buffer limit]`)
    this.pending = ''
  }

  private emitIndexedLine(line: string): void {
    this.lineSequence += 1
    emitRuntimeLogLine(line, {
      sourceKey: `live:${process.pid}:${this.lineSequence}`
    })
  }
}

export function emitRuntimeLogLine(line: string, options?: RuntimeLogLineSinkOptions): void {
  if (runtimeLogLineSink) {
    runtimeLogLineSink(line, options)
    return
  }

  enqueuePendingRuntimeLogLine(line, options)
}

function enqueuePendingRuntimeLogLine(line: string, options?: RuntimeLogLineSinkOptions): void {
  const lineBytes = Buffer.byteLength(line, 'utf8')
  while (
    pendingRuntimeLogLines.length >= maxPendingQueueLines
    || pendingRuntimeLogLineBytes + lineBytes > maxPendingQueueBytes
  ) {
    const removed = pendingRuntimeLogLines.shift()
    if (removed === undefined) break
    pendingRuntimeLogLineBytes = Math.max(0, pendingRuntimeLogLineBytes - Buffer.byteLength(removed.line, 'utf8'))
  }
  pendingRuntimeLogLines.push({ line, options })
  pendingRuntimeLogLineBytes += lineBytes
}
