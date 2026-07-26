import { errorLogFields, logger } from '../../shared/logger.js'
import type { CodexContextStorageCleanupSettlement } from '../../storage/codex-context-state.repository.js'
import {
  deleteCodexContextStorageKeys,
  type CodexContextStorageKeyDeletionResult
} from '../gateway/codex-responses/chat-bridge-state.js'

interface CodexContextStorageCleanupDependencies {
  deleteStorageKeys: (storageKeys: readonly string[]) => Promise<CodexContextStorageKeyDeletionResult>
  settle: (settlement: CodexContextStorageCleanupSettlement) => Promise<unknown>
}

const defaultDependencies: CodexContextStorageCleanupDependencies = {
  deleteStorageKeys: deleteCodexContextStorageKeys,
  settle: async (settlement) => {
    const { requestBackgroundWorkerDbService } = await import('./background-ipc.js')
    return await requestBackgroundWorkerDbService({
      type: 'settle_codex_context_storage_cleanup',
      ...settlement
    })
  }
}

export async function processCodexContextStorageCleanupBatch(input: {
  storageKeys: readonly string[]
  signal: AbortSignal
  dependencies?: CodexContextStorageCleanupDependencies
}): Promise<number> {
  const dependencies = input.dependencies ?? defaultDependencies
  const deletion = await dependencies.deleteStorageKeys(input.storageKeys)
  await dependencies.settle({
    succeededStorageKeys: deletion.succeededStorageKeys,
    failures: deletion.failures
  })
  if (deletion.failures.length > 0) {
    logger.warn(errorLogFields(new Error('部分 Codex Context 状态文件删除失败'), {
      event: 'codex_context_storage_cleanup_deferred',
      failedCount: deletion.failures.length
    }), 'Codex Context 状态文件删除失败，已持久化等待重试')
  }
  input.signal.throwIfAborted()
  return deletion.deleted
}
