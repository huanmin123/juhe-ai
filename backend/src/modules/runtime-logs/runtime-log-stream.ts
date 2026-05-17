import { Writable } from 'node:stream'

type RuntimeLogLineSink = (line: string) => void

let runtimeLogLineSink: RuntimeLogLineSink | undefined
const pendingRuntimeLogLines: string[] = []
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
  for (const line of lines) {
    runtimeLogLineSink(line)
  }
}

export class RuntimeLogIndexStream extends Writable {
  private pending = ''

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
      this.emitLine(line)
      newlineIndex = this.pending.indexOf('\n')
    }

    if (includePartial && this.pending.trim()) {
      this.emitLine(this.pending)
      this.pending = ''
    }
  }

  private emitLine(line: string): void {
    if (runtimeLogLineSink) {
      runtimeLogLineSink(line)
      return
    }

    enqueuePendingRuntimeLogLine(line)
  }

  private truncateOversizedPendingLine(): void {
    if (Buffer.byteLength(this.pending, 'utf8') <= maxPendingLineBytes) {
      return
    }
    this.emitLine(`${this.pending.slice(0, maxPendingLineBytes)} [truncated: runtime log line exceeded pending buffer limit]`)
    this.pending = ''
  }
}

function enqueuePendingRuntimeLogLine(line: string): void {
  const lineBytes = Buffer.byteLength(line, 'utf8')
  while (
    pendingRuntimeLogLines.length >= maxPendingQueueLines
    || pendingRuntimeLogLineBytes + lineBytes > maxPendingQueueBytes
  ) {
    const removed = pendingRuntimeLogLines.shift()
    if (removed === undefined) break
    pendingRuntimeLogLineBytes = Math.max(0, pendingRuntimeLogLineBytes - Buffer.byteLength(removed, 'utf8'))
  }
  pendingRuntimeLogLines.push(line)
  pendingRuntimeLogLineBytes += lineBytes
}
