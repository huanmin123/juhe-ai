import {
  getRuntimeLogFileImportRuntime as getRuntimeLogFileImportRuntimeService,
  startRuntimeLogFileImport as startRuntimeLogFileImportService,
  stopRuntimeLogFileImport as stopRuntimeLogFileImportService
} from './runtime-log-file-import.service.js'

// Keep the worker lifecycle owned by the complete runtime-log index feature.
export function startRuntimeLogFileImport(): void {
  startRuntimeLogFileImportService()
}

export function stopRuntimeLogFileImport(options?: { drainTimeoutMs?: number }): Promise<void> {
  return stopRuntimeLogFileImportService(options)
}

export function getRuntimeLogFileImportRuntime() {
  return getRuntimeLogFileImportRuntimeService()
}
