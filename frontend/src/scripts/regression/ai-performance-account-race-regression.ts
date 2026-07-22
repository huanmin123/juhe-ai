import assert from 'node:assert/strict'
import { ref } from 'vue'

import { api } from '@/api/client'
import type { AiPerformanceAccountOption, AiPerformanceOverview } from '@/types/domain'
import { useAiPerformanceAccountSelection } from '@/views/ai-performance/useAiPerformanceAccountSelection'

const originalLoader = api.stats.aiPerformanceAccounts
const request = deferred<AiPerformanceAccountOption[]>()
api.stats.aiPerformanceAccounts = async () => request.promise

try {
  const resource = useAiPerformanceAccountSelection({
    isManagementView: ref(true),
    isPageActive: () => true,
    overview: ref<AiPerformanceOverview>(),
    reloadPerformance: () => undefined,
    requestRender: () => undefined,
    selectedSystemAccountId: () => 'system-a'
  })
  const staleLoad = resource.loadAccounts()
  assert.equal(resource.accountsLoading.value, true)
  resource.clearAccountState()
  assert.equal(resource.accountsLoading.value, false)
  request.resolve([{ id: 'account-old', name: '旧账户' } as AiPerformanceAccountOption])
  await staleLoad
  assert.deepEqual(resource.accounts.value, [])
} finally {
  api.stats.aiPerformanceAccounts = originalLoader
}

console.log('AI 性能账户选项竞态回归通过：清空作用域会废弃并释放在途请求')

interface Deferred<T> { promise: Promise<T>; resolve: (value: T) => void }
function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}
