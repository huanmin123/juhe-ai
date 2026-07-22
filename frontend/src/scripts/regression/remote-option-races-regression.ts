import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { ref } from 'vue'

import { api } from '@/api/client'
import { useRemoteAuthorizationPrincipalOptions } from '@/composables/useRemoteAuthorizationPrincipalOptions'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import type { AccountOptionSummary, GroupOptionSummary, SystemAccountPrincipalSummary } from '@/types/domain'
import { useAuditLogAccountOptions } from '@/views/audit-logs/useAuditLogAccountOptions'
import { useUsageRecordGroupOptions } from '@/views/usage-records/useUsageRecordGroupOptions'

const originalConsoleWarn = console.warn
const originalSystemOptions = api.systemAccounts.options
const originalAuthorizationAccounts = api.authorizationOptions.granteeAccounts
const originalAccountOptions = api.accounts.options
console.warn = () => undefined

try {
  await verifyRemoteSystemAccountInvalidation()
  await verifyRemoteAuthorizationInvalidation()
  await verifyAuditAccountInvalidation()
  await verifyUsageGroupScopeIsolation()
  verifyAuthorizationAllBranchInvalidates()
} finally {
  api.systemAccounts.options = originalSystemOptions
  api.authorizationOptions.granteeAccounts = originalAuthorizationAccounts
  api.accounts.options = originalAccountOptions
  console.warn = originalConsoleWarn
}

console.log('远程筛选项竞态回归通过：禁用/失去作用域会废弃旧请求，缺失项副作用只由当前作用域触发')

async function verifyRemoteSystemAccountInvalidation(): Promise<void> {
  const enabled = ref(true)
  const selectedId = ref('system-old')
  const missingIds: string[][] = []
  const listRequest = deferred<SystemAccountPrincipalSummary[]>()
  const selectedRequest = deferred<SystemAccountPrincipalSummary[]>()
  let calls = 0
  api.systemAccounts.options = async () => (++calls === 1 ? listRequest.promise : selectedRequest.promise)
  const resource = useRemoteSystemAccountOptions({
    enabled: () => enabled.value,
    onMissingSelectedIds: (ids) => missingIds.push(ids),
    selectedIds: () => [selectedId.value]
  })

  const staleLoad = resource.load()
  listRequest.resolve([])
  await waitFor(() => calls === 2)
  enabled.value = false
  await resource.load()
  assert.equal(resource.loading.value, false)
  selectedRequest.resolve([])
  await staleLoad
  assert.deepEqual(resource.systemAccounts.value, [])
  assert.deepEqual(missingIds, [], '失效请求不得移除系统账户选择')
}

async function verifyRemoteAuthorizationInvalidation(): Promise<void> {
  const enabled = ref(true)
  const missingIds: string[][] = []
  const listRequest = deferred<SystemAccountPrincipalSummary[]>()
  const selectedRequest = deferred<SystemAccountPrincipalSummary[]>()
  let calls = 0
  api.authorizationOptions.granteeAccounts = async () => (++calls === 1 ? listRequest.promise : selectedRequest.promise)
  const resource = useRemoteAuthorizationPrincipalOptions<SystemAccountPrincipalSummary>({
    enabled: () => enabled.value,
    isManagementView: () => true,
    kind: 'account',
    onMissingSelectedIds: (ids) => missingIds.push(ids),
    selectedIds: () => ['authorization-old']
  })

  const staleLoad = resource.load()
  listRequest.resolve([])
  await waitFor(() => calls === 2)
  enabled.value = false
  await resource.load()
  assert.equal(resource.loading.value, false)
  selectedRequest.resolve([])
  await staleLoad
  assert.deepEqual(resource.options.value, [])
  assert.deepEqual(missingIds, [], '失效请求不得移除授权对象选择')
}

async function verifyAuditAccountInvalidation(): Promise<void> {
  const systemAccountId = ref<string | undefined>('system-a')
  const selection = ref({ id: 'account-a', name: '账户 A' })
  const listRequest = deferred<AccountOptionSummary[]>()
  api.accounts.options = async () => listRequest.promise
  const resource = useAuditLogAccountOptions({
    accountSelection: selection,
    selectedAccountId: () => 'account-a',
    selectedSystemAccountId: () => systemAccountId.value
  })

  const staleLoad = resource.load()
  systemAccountId.value = undefined
  await resource.load()
  assert.equal(resource.loading.value, false)
  assert.equal(selection.value, undefined)
  listRequest.resolve([{ id: 'account-a', name: '旧账户' } as AccountOptionSummary])
  await staleLoad
  assert.deepEqual(resource.options.value, [])
}

async function verifyUsageGroupScopeIsolation(): Promise<void> {
  const systemAccountId = ref('system-a')
  const selectedId = ref('group-a')
  const selection = ref({ id: 'group-a', name: '分组 A' })
  const oldList = deferred<GroupOptionSummary[]>()
  const oldSelected = deferred<GroupOptionSummary[]>()
  const newList = deferred<GroupOptionSummary[]>()
  const missingScopes: string[] = []
  let calls = 0
  const resource = useUsageRecordGroupOptions({
    groupFilterSelection: selection,
    groupsApi: {
      options: async () => {
        calls += 1
        if (calls === 1) return oldList.promise
        if (calls === 2) return oldSelected.promise
        return newList.promise
      }
    },
    isManagementView: ref(true),
    onSelectedGroupMissing: () => missingScopes.push(systemAccountId.value),
    selectedGroupId: () => selectedId.value,
    systemAccountId: () => systemAccountId.value
  })

  const staleLoad = resource.load()
  oldList.resolve([])
  await waitFor(() => calls === 2)
  systemAccountId.value = 'system-b'
  selectedId.value = undefined as unknown as string
  const currentLoad = resource.load()
  newList.resolve([{ id: 'group-b', name: '分组 B' } as GroupOptionSummary])
  await currentLoad
  oldSelected.resolve([])
  await staleLoad
  assert.deepEqual(resource.groups.value.map((item) => item.id), ['group-b'])
  assert.deepEqual(missingScopes, [], '旧系统账户作用域不得触发缺失分组清理')
}

function verifyAuthorizationAllBranchInvalidates(): void {
  const currentDir = dirname(fileURLToPath(import.meta.url))
  const source = readFileSync(resolve(currentDir, '../../views/authorizations/useAuthorizationUsageResourceFilters.ts'), 'utf8')
  assert.match(source, /if \(filters\.resourceType === 'all'\) \{\s*requestId \+= 1[\s\S]*resourceOptionsLoading\.value = false/)
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}

async function waitFor(predicate: () => boolean): Promise<void> {
  for (let index = 0; index < 50; index += 1) {
    if (predicate()) return
    await Promise.resolve()
  }
  throw new Error('等待远程选项请求超时')
}
