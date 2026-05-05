import { Writable } from 'node:stream'

type RuntimeLogLineSink = (line: string) => void

let runtimeLogLineSink: RuntimeLogLineSink | undefined
const pendingRuntimeLogLines: string[] = []

export function setRuntimeLogLineSink(sink?: RuntimeLogLineSink): void {
  runtimeLogLineSink = sink
  if (!runtimeLogLineSink) {
    return
  }

  while (pendingRuntimeLogLines.length > 0) {
    runtimeLogLineSink(pendingRuntimeLogLines.shift() ?? '')
  }
}

export class RuntimeLogIndexStream extends Writable {
  private pending = ''

  _write(chunk: Buffer | string, encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    try {
      const text = Buffer.isBuffer(chunk) ? chunk.toString('utf8') : Buffer.from(chunk, encoding).toString('utf8')
      this.pending += text
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

    pendingRuntimeLogLines.push(line)
  }
}
