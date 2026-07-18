import type { Writable } from 'node:stream'

const guardedOutputs = new WeakSet<Writable>()

export function forwardSupervisorOutput(destination: Writable, chunk: Buffer | string): boolean {
  guardSupervisorOutput(destination)
  if (destination.destroyed || destination.writableEnded || !destination.writable) {
    return false
  }
  try {
    destination.write(chunk, (error) => {
      if (error) reportSupervisorOutputError(error)
    })
    return true
  } catch (error) {
    reportSupervisorOutputError(error instanceof Error ? error : new Error(String(error)))
    return false
  }
}

function guardSupervisorOutput(destination: Writable): void {
  if (guardedOutputs.has(destination)) return
  guardedOutputs.add(destination)
  destination.on('error', (error) => {
    reportSupervisorOutputError(error)
  })
}

function reportSupervisorOutputError(error: Error | null | undefined): void {
  const code = (error as NodeJS.ErrnoException | undefined)?.code
  if (code === 'EPIPE' || code === 'ERR_STREAM_DESTROYED') {
    return
  }
  ;(process as unknown as { _rawDebug?: (message: string) => void })._rawDebug?.(`[supervisor-output] 输出转发失败：${error?.message ?? 'unknown error'}`)
}
