import { message } from '@/lib/antd'
import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import type { AccountSummary, GroupSummary } from '@/types/domain'
import type { AuthorizationFilterResourceType } from './authorizationTableColumns'

export type AuthorizationUsageResourceFilters = {
  resourceOwnerSystemAccountId: string
  resourceType: AuthorizationFilterResourceType
  resourceId?: string
}

export function useAuthorizationUsageResourceFilters(filters: AuthorizationUsageResourceFilters) {
  const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
  const accounts = ref<AccountSummary[]>([])
  const groups = ref<GroupSummary[]>([])
  const selectedResourceOwnerSystemAccountId = computed(() => {
    return isManagementView.value ? scopedSystemAccountId(filters.resourceOwnerSystemAccountId) : undefined
  })
  const ownAuthorizableAccounts = computed(() => accounts.value.filter((account) => account.permissions?.canAuthorize !== false))
  const ownAuthorizableGroups = computed(() => groups.value.filter((group) => group.permissions?.canAuthorize !== false))
  const resourceOptions = computed(() => {
    if (filters.resourceType === 'all') return []
    if (filters.resourceType === 'account') {
      return ownAuthorizableAccounts.value
        .filter((account) => matchesSelectedResourceOwner(account))
        .map((account) => ({ label: account.name, value: account.id }))
    }
    return ownAuthorizableGroups.value
      .filter((group) => matchesSelectedResourceOwner(group))
      .map((group) => ({ label: group.name, value: group.id }))
  })

  async function loadAuthorizableResourceOptions() {
    const ownerSystemAccountId = selectedResourceOwnerSystemAccountId.value
    const [accountResult, groupResult] = await Promise.allSettled([
      isManagementView.value ? api.accounts.list({ systemAccountId: ownerSystemAccountId, limit: 500 }) : api.myAccounts.list({ limit: 500 }),
      isManagementView.value ? api.groups.list({ systemAccountId: ownerSystemAccountId }) : api.myGroups.list()
    ])
    if (accountResult.status === 'fulfilled') {
      accounts.value = accountResult.value.items
    } else {
      console.error(accountResult.reason)
      message.error('加载 AI 账户失败')
    }
    if (groupResult.status === 'fulfilled') {
      groups.value = groupResult.value
    } else {
      console.error(groupResult.reason)
      message.error('加载分组失败')
    }
  }

  function resetResourceId() {
    filters.resourceId = undefined
  }

  function matchesSelectedResourceOwner(resource: Pick<AccountSummary | GroupSummary, 'ownerSystemAccountId' | 'systemAccountId'>): boolean {
    const ownerSystemAccountId = selectedResourceOwnerSystemAccountId.value
    if (!ownerSystemAccountId) return true
    return (resource.ownerSystemAccountId ?? resource.systemAccountId) === ownerSystemAccountId
  }

  return {
    isManagementView,
    scopedSystemAccountId,
    accounts,
    groups,
    selectedResourceOwnerSystemAccountId,
    resourceOptions,
    loadAuthorizableResourceOptions,
    resetResourceId
  }
}
