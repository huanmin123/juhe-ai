import { existsSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { relative, resolve, sep } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type { CodexContextStorageCleanupSettlement } from '../../storage/codex-context-state.repository.js'

export interface CodexContextStorageKeyDeletionResult {
  deleted: number
  succeededStorageKeys: string[]
  failures: Array<{ storageKey: string; error: string }>
}

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

export async function deleteCodexContextStorageKeys(storageKeys: readonly string[]): Promise<CodexContextStorageKeyDeletionResult> {
  let deleted = 0
  const succeededStorageKeys: string[] = []
  const failures: Array<{ storageKey: string; error: string }> = []
  for (const storageKey of [...new Set(storageKeys)]) {
    try {
      const path = resolveCodexContextStorageCleanupPath(storageKey)
      if (existsSync(path)) {
        await rm(path, { force: true })
        deleted += 1
      }
      succeededStorageKeys.push(storageKey)
    } catch (error) {
      failures.push({
        storageKey,
        error: error instanceof Error ? error.message : String(error)
      })
    }
  }
  return { deleted, succeededStorageKeys, failures }
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

function resolveCodexContextStorageCleanupPath(storageKey: string): string {
  const normalizedKey = storageKey.replace(/\\/g, '/').replace(/^\/+/, '')
  if (normalizedKey.includes('..')) {
    throw new Error('Responses 桥接状态 storage key 非法')
  }
  const root = resolve(runtimeConfig.codexContextRoot)
  const target = resolve(root, normalizedKey)
  const rel = relative(root, target)
  if (!rel || rel.startsWith('..') || rel.startsWith(`..${sep}`) || resolve(root, rel) !== target) {
    throw new Error('Responses 桥接状态 storage key 超出数据目录')
  }
  return target
}
