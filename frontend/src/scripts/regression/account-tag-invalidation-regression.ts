import assert from 'node:assert/strict'
import { computed } from 'vue'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { api } from '@/api/client'
import type { AccountTagSummary } from '@/types/domain'
import { useAccountFilterTagOptions } from '@/views/accounts/useAccountFilterTagOptions'

const originalTags = api.myAccounts.tags
const oldRequest = deferred<AccountTagSummary[]>()
let calls = 0
api.myAccounts.tags = async () => {
  calls += 1
  return calls === 1 ? oldRequest.promise : [{ id: 'tag-new', name: '新标签', accountCount: 1 }]
}

try {
  const resource = useAccountFilterTagOptions({
    accountScopeParams: computed(() => undefined),
    isManagementView: computed(() => false)
  })
  const staleLoad = resource.load()
  resource.invalidate()
  oldRequest.resolve([{ id: 'tag-old', name: '旧标签', accountCount: 1 }])
  await staleLoad
  assert.deepEqual(resource.options.value, [], '标签失效后旧响应不得重新写回')
  await resource.load()
  assert.deepEqual(resource.options.value.map((item) => item.id), ['tag-new'])
  verifySaveFlowWiring()
} finally {
  api.myAccounts.tags = originalTags
}

console.log('账户标签失效回归通过：保存/删除会使编辑与筛选标签资源失效，旧响应不能回写')

function verifySaveFlowWiring(): void {
  const currentDir = dirname(fileURLToPath(import.meta.url))
  const saveFlow = readFileSync(resolve(currentDir, '../../views/accounts/useAccountEditSaveFlow.ts'), 'utf8')
  const editForm = readFileSync(resolve(currentDir, '../../views/accounts/useAccountEditForm.ts'), 'utf8')
  assert.match(saveFlow, /options\.invalidateAccountTagOptions\(scopeParams\)/)
  assert.match(editForm, /invalidateAccountTagOptions\(scopeParams\)[\s\S]*options\.invalidateFilterAccountTagOptions\(\)/)
}

interface Deferred<T> { promise: Promise<T>; resolve: (value: T) => void }
function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}
