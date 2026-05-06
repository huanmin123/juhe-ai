import { createReadStream, existsSync } from 'node:fs'
import { join } from 'node:path'
import { createInterface } from 'node:readline'

import { runtimeConfig } from '../../config/runtime.js'
import { flushRuntimeLogIndexQueue, enqueueRuntimeLogLineLocal } from './runtime-log-index-queue.service.js'

let importStarted = false

export function startRuntimeLogFileImport(): void {
  if (importStarted || !runtimeConfig.log.fileEnabled) {
    return
  }
  importStarted = true

  setImmediate(() => {
    void importActiveRuntimeLogFile()
  })
}

async function importActiveRuntimeLogFile(): Promise<void> {
  for (const logPath of activeRuntimeLogPaths()) {
    await importRuntimeLogFile(logPath)
  }
}

function activeRuntimeLogPaths(): string[] {
  return [
    join(runtimeConfig.log.directory, 'juhe-ai.log'),
    join(runtimeConfig.log.directory, 'juhe-ai.worker.log')
  ]
}

async function importRuntimeLogFile(logPath: string): Promise<void> {
  if (!existsSync(logPath)) {
    return
  }

  const stream = createReadStream(logPath, { encoding: 'utf8' })
  const lines = createInterface({ input: stream, crlfDelay: Infinity })
  try {
    for await (const line of lines) {
      enqueueRuntimeLogLineLocal(line)
    }
    flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false })
  } catch (error) {
    process.stderr.write(`[runtime-log-index] 导入当前日志文件失败 ${logPath}：${error instanceof Error ? error.message : String(error)}\n`)
  }
}
