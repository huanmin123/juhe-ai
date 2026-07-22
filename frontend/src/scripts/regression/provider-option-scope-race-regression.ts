import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { api } from '@/api/client'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import type { ProviderDefinition } from '@/types/domain'

const originalDefinitions = api.providers.definitions
const first = deferred<ProviderDefinition[]>()
const second = deferred<ProviderDefinition[]>()
let callCount = 0
let generation = 1
const applied: string[] = []
api.providers.definitions = async () => (++callCount === 1 ? first.promise : second.promise)

try {
  const oldLoad = loadProviderOptionsResource({
    apply: (items) => applied.push(items[0]?.code ?? ''),
    includeDefinitions: true,
    isCurrent: () => generation === 1,
    isManagementView: false
  })
  generation = 2
  const newLoad = loadProviderOptionsResource({
    apply: (items) => applied.push(items[0]?.code ?? ''),
    includeDefinitions: true,
    isCurrent: () => generation === 2,
    isManagementView: false
  })
  second.resolve([provider('new')])
  await newLoad
  first.resolve([provider('old')])
  await oldLoad
  assert.deepEqual(applied, ['new'])
  verifyPageGuards()
} finally {
  api.providers.definitions = originalDefinitions
}

console.log('供应商选项作用域竞态回归通过：旧慢响应不会覆盖 Providers/Groups/UsageStats/Accounts 的新作用域')

function verifyPageGuards(): void {
  const currentDir = dirname(fileURLToPath(import.meta.url))
  for (const relative of [
    '../../views/providers/ProvidersView.vue',
    '../../views/groups/GroupsView.vue',
    '../../views/usage-stats/UsageStatsView.vue',
    '../../views/accounts/useAccountListData.ts'
  ]) {
    const source = readFileSync(resolve(currentDir, relative), 'utf8')
    assert.match(source, /isCurrent:/, `${relative} 必须向供应商资源传入当前请求校验`)
  }
}

function provider(code: string): ProviderDefinition {
  return { code, id: code, name: code } as ProviderDefinition
}

interface Deferred<T> { promise: Promise<T>; resolve: (value: T) => void }
function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}
