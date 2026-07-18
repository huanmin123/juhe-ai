import { computed, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { rememberAccountLabels, rememberAccountSelection } from '@/shared/accountLabelCache'
import { rememberGroupLabels } from '@/shared/groupLabelCache'
import type { AccountOptionSummary, GroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import type { AuthorizationCreateFormModel } from './authorizationFormModel'
import {
  normalizeSearchKeyword,
  selectedAccountFromOptions,
  selectedGroupFromOptions,
  selectedTeamFromOptions,
  selectedUserFromOptions
} from './authorizationOptionHelpers'
import type { AuthorizationFilters } from './authorizationPageState'
import { createAuthorizationSearchScheduler } from './authorizationSearchScheduler'
import {
  ensureSelectedAccountOption,
  ensureSelectedAuthorizationGranteeGroupOption,
  ensureSelectedGroupOption,
  ensureSelectedSystemAccountPrincipal,
  ensureSelectedTeamOption
} from './authorizationSelectedOptionLoaders'
import { loadAuthorizationOptionResource } from './authorizationOptionResource'

const remoteOptionLimit = 50
const remoteSearchDelayMs = 250

export interface UseAuthorizationOptionStateOptions {
  createExcludedGranteeIds: ComputedRef<string[]>
  createForm: AuthorizationCreateFormModel
  filters: AuthorizationFilters
  isManagementView: ComputedRef<boolean>
  selectedFilterOwnerSystemAccountId: ComputedRef<string | undefined>
}

export function useAuthorizationOptionState(options: UseAuthorizationOptionStateOptions) {
  const { createExcludedGranteeIds, createForm, filters, isManagementView, selectedFilterOwnerSystemAccountId } = options

  const accounts = ref<AccountOptionSummary[]>([])
  const groups = ref<GroupOptionSummary[]>([])
  const createAccounts = ref<AccountOptionSummary[]>([])
  const createGroups = ref<GroupOptionSummary[]>([])
  const createTargetGroups = ref<GroupOptionSummary[]>([])
  const teams = ref<SystemTeamPrincipalSummary[]>([])
  const users = ref<SystemAccountPrincipalSummary[]>([])
  const createOwnerUsers = ref<SystemAccountPrincipalSummary[]>([])
  const createUsers = ref<SystemAccountPrincipalSummary[]>([])
  const createTeams = ref<SystemTeamPrincipalSummary[]>([])
  const createOwnerUsersLoading = ref(false)
  const createResourceOptionsLoading = ref(false)
  const createGranteeOptionsLoading = ref(false)
  const createTargetGroupOptionsLoading = ref(false)
  const filterResourceOptionsLoading = ref(false)
  const filterTeamOptionsLoading = ref(false)
  const filterUserOptionsLoading = ref(false)
  const createOwnerSearchKeyword = ref('')
  const createResourceSearchKeyword = ref('')
  const createGranteeSearchKeyword = ref('')
  const createTargetGroupSearchKeyword = ref('')
  const filterResourceSearchKeyword = ref('')
  const filterTeamSearchKeyword = ref('')
  const filterUserSearchKeyword = ref('')

  let createOwnerUserRequestId = 0
  let createOwnerResourceRequestId = 0
  let createGranteeRequestId = 0
  let createTargetGroupRequestId = 0
  let filterResourceRequestId = 0
  let filterTeamRequestId = 0
  let filterUserRequestId = 0

  const filterResourceDisabled = computed(() => {
    return isManagementView.value && filters.resourceType === 'group' && !selectedFilterOwnerSystemAccountId.value
  })
  const createOwnedAccounts = computed(() => createAccounts.value.filter((account) => account.permissions?.canAuthorize !== false))
  const createOwnedGroups = computed(() => createGroups.value.filter((group) => group.permissions?.canAuthorize !== false))
  const selectedCreateAccount = computed(() => createForm.resourceType === 'account'
    ? createOwnedAccounts.value.find((account) => account.id === createForm.resourceId)
    : undefined)
  const createTargetGroupVisible = computed(() => createForm.resourceType === 'account' && createForm.granteeType === 'system_account')

  async function loadCreateOwnerOptions(keyword?: string): Promise<void> {
    if (!isManagementView.value) {
      createOwnerUsers.value = []
      return
    }
    const search = normalizeSearchKeyword(keyword)
    const requestId = ++createOwnerUserRequestId
    createOwnerUsersLoading.value = true
    try {
      await loadAuthorizationOptionResource<SystemAccountPrincipalSummary[]>({
        apply: (nextOptions) => { createOwnerUsers.value = nextOptions },
        domain: 'systemAccounts.options',
        isCurrent: () => requestId === createOwnerUserRequestId,
        isManagementView: true,
        loadNetwork: async () => {
          let nextOptions = await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
          nextOptions = await ensureSelectedSystemAccountPrincipal(nextOptions, createForm.ownerSystemAccountId, isManagementView.value)
          return nextOptions
        },
        query: { purpose: 'create-owner', search, selectedId: createForm.ownerSystemAccountId, limit: remoteOptionLimit },
        route: '/authorization-options/grantee-accounts'
      })
    } catch (error) {
      if (requestId !== createOwnerUserRequestId) return
      console.error(error)
      message.error('加载授权人列表失败')
    } finally {
      if (requestId === createOwnerUserRequestId) {
        createOwnerUsersLoading.value = false
      }
    }
  }

  async function loadCreateResourceOptions(keyword?: string): Promise<void> {
    const ownerSystemAccountId = createForm.ownerSystemAccountId
    if (isManagementView.value && !ownerSystemAccountId) {
      createAccounts.value = []
      createGroups.value = []
      return
    }
    const search = normalizeSearchKeyword(keyword)
    const resourceType: 'account' | 'group' = createForm.resourceType === 'account' ? 'account' : 'group'
    const requestId = ++createOwnerResourceRequestId
    createResourceOptionsLoading.value = true
    try {
      const route = resourceType === 'account'
        ? (isManagementView.value ? '/accounts/options' : '/my-accounts/options')
        : (isManagementView.value ? '/groups/options' : '/my-groups/options')
      await loadAuthorizationOptionResource<AccountOptionSummary[] | GroupOptionSummary[]>({
        apply: (nextOptions) => {
          if (resourceType === 'account') {
            const nextAccounts = nextOptions as AccountOptionSummary[]
            rememberAccountLabels(nextAccounts)
            syncCreateResourceAccount(nextAccounts)
            createAccounts.value = nextAccounts
            createGroups.value = []
          } else {
            const nextGroups = nextOptions as GroupOptionSummary[]
            rememberGroupLabels(nextGroups)
            syncCreateResourceGroup(nextGroups)
            createGroups.value = nextGroups
            createAccounts.value = []
          }
        },
        domain: resourceType === 'account' ? 'accounts.options' : 'groups.static',
        isCurrent: () => requestId === createOwnerResourceRequestId && createForm.resourceType === resourceType,
        isManagementView: isManagementView.value,
        loadNetwork: async () => {
          if (resourceType === 'account') {
            let nextAccounts = isManagementView.value
              ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
              : await api.myAccounts.options({ keyword: search, limit: remoteOptionLimit })
            return await ensureSelectedAccountOption(nextAccounts, createForm.resourceId, ownerSystemAccountId, isManagementView.value)
          }
          let nextGroups = isManagementView.value
            ? await api.groups.options({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
            : await api.myGroups.options({ keyword: search, limit: remoteOptionLimit })
          return await ensureSelectedGroupOption(nextGroups, createForm.resourceId, ownerSystemAccountId, isManagementView.value)
        },
        query: { purpose: 'create-resource', resourceType, ownerSystemAccountId, search, selectedId: createForm.resourceId, limit: remoteOptionLimit },
        route,
        targetSystemAccountId: ownerSystemAccountId
      })
    } catch (error) {
      if (requestId !== createOwnerResourceRequestId) return
      console.error(error)
      message.error('加载授权资源失败')
    } finally {
      if (requestId === createOwnerResourceRequestId) {
        createResourceOptionsLoading.value = false
      }
    }
  }

  async function loadCreateGranteeOptions(keyword?: string): Promise<void> {
    const search = normalizeSearchKeyword(keyword)
    const excludedGranteeIds = [...createExcludedGranteeIds.value].sort()
    const granteeType: 'system_account' | 'team' = createForm.granteeType === 'system_account' ? 'system_account' : 'team'
    const requestId = ++createGranteeRequestId
    createGranteeOptionsLoading.value = true
    try {
      const route = `/${isManagementView.value ? '' : 'my-'}authorization-options/grantee-${granteeType === 'system_account' ? 'accounts' : 'teams'}`
      await loadAuthorizationOptionResource<SystemAccountPrincipalSummary[] | SystemTeamPrincipalSummary[]>({
        apply: (nextOptions) => {
          if (granteeType === 'system_account') {
            createUsers.value = nextOptions as SystemAccountPrincipalSummary[]
            createTeams.value = []
          } else {
            createTeams.value = nextOptions as SystemTeamPrincipalSummary[]
            createUsers.value = []
          }
        },
        domain: granteeType === 'system_account' ? 'systemAccounts.options' : 'teams.options',
        isCurrent: () => requestId === createGranteeRequestId && createForm.granteeType === granteeType,
        isManagementView: isManagementView.value,
        loadNetwork: async () => {
          if (granteeType === 'system_account') {
            let nextUsers = isManagementView.value
              ? await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
              : await api.myAuthorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
            nextUsers = nextUsers.filter((user) => !excludedGranteeIds.includes(user.id))
            return await ensureSelectedSystemAccountPrincipal(nextUsers, createForm.granteeId, isManagementView.value)
          }
          let nextTeams = isManagementView.value
            ? await api.authorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
            : await api.myAuthorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
          return await ensureSelectedTeamOption(nextTeams, createForm.granteeId, isManagementView.value)
        },
        query: { purpose: 'create-grantee', granteeType, search, selectedId: createForm.granteeId, excludedGranteeIds, limit: remoteOptionLimit },
        route
      })
    } catch (error) {
      if (requestId !== createGranteeRequestId) return
      console.error(error)
      message.error('加载授权对象失败')
    } finally {
      if (requestId === createGranteeRequestId) {
        createGranteeOptionsLoading.value = false
      }
    }
  }

  async function loadCreateTargetGroupOptions(keyword?: string): Promise<void> {
    const granteeSystemAccountId = createForm.granteeType === 'system_account' ? createForm.granteeId : ''
    const providerCode = selectedCreateAccount.value?.providerCode
    if (!createTargetGroupVisible.value || !granteeSystemAccountId || !providerCode) {
      createTargetGroupRequestId += 1
      createTargetGroupOptionsLoading.value = false
      createTargetGroups.value = []
      return
    }
    const search = normalizeSearchKeyword(keyword)
    const requestId = ++createTargetGroupRequestId
    createTargetGroupOptionsLoading.value = true
    try {
      await loadAuthorizationOptionResource<GroupOptionSummary[]>({
        apply: (nextGroups) => {
          rememberGroupLabels(nextGroups)
          syncCreateTargetGroup(nextGroups)
          selectDefaultCreateTargetGroup(nextGroups)
          createTargetGroups.value = nextGroups
        },
        domain: 'groups.static',
        isCurrent: () => requestId === createTargetGroupRequestId
          && createForm.granteeId === granteeSystemAccountId
          && selectedCreateAccount.value?.providerCode === providerCode,
        isManagementView: isManagementView.value,
        loadNetwork: async () => {
          let nextGroups = isManagementView.value
            ? await api.authorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
            : await api.myAuthorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
          return await ensureSelectedAuthorizationGranteeGroupOption(nextGroups, createForm.targetGroupId, granteeSystemAccountId, providerCode, isManagementView.value)
        },
        query: { purpose: 'create-target-group', granteeSystemAccountId, providerCode, search, selectedId: createForm.targetGroupId, preferDefault: true, limit: remoteOptionLimit },
        route: `/${isManagementView.value ? '' : 'my-'}authorization-options/grantee-groups`,
        targetSystemAccountId: isManagementView.value ? granteeSystemAccountId : undefined
      })
    } catch (error) {
      if (requestId !== createTargetGroupRequestId) return
      console.error(error)
      message.error('加载目标分组失败')
    } finally {
      if (requestId === createTargetGroupRequestId) {
        createTargetGroupOptionsLoading.value = false
      }
    }
  }

  async function loadFilterResourceOptions(keyword?: string): Promise<void> {
    if (!isManagementView.value) {
      accounts.value = []
      groups.value = []
      return
    }
    if (filters.resourceType === 'all') {
      accounts.value = []
      groups.value = []
      return
    }
    if (filterResourceDisabled.value) {
      filterResourceRequestId += 1
      filterResourceOptionsLoading.value = false
      accounts.value = []
      groups.value = []
      resetFilterResource()
      return
    }
    const requestKeyword = normalizeSearchKeyword(keyword)
    const systemAccountId = selectedFilterOwnerSystemAccountId.value
    const resourceType: 'account' | 'group' = filters.resourceType === 'account' ? 'account' : 'group'
    const requestId = ++filterResourceRequestId
    filterResourceOptionsLoading.value = true
    try {
      await loadAuthorizationOptionResource<AccountOptionSummary[] | GroupOptionSummary[]>({
        apply: (nextOptions) => {
          if (resourceType === 'account') {
            const nextAccounts = nextOptions as AccountOptionSummary[]
            rememberAccountLabels(nextAccounts)
            syncFilterResourceAccount(nextAccounts)
            accounts.value = nextAccounts
            groups.value = []
          } else {
            const nextGroups = nextOptions as GroupOptionSummary[]
            rememberGroupLabels(nextGroups)
            syncFilterResourceGroup(nextGroups)
            groups.value = nextGroups
            accounts.value = []
          }
        },
        domain: resourceType === 'account' ? 'accounts.options' : 'groups.static',
        isCurrent: () => requestId === filterResourceRequestId && filters.resourceType === resourceType,
        isManagementView: true,
        loadNetwork: async () => {
          if (resourceType === 'account') {
            const nextAccounts = await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
            return await ensureSelectedAccountOption(nextAccounts, filters.resourceId, systemAccountId, isManagementView.value)
          }
          const nextGroups = await api.groups.options({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
          return await ensureSelectedGroupOption(nextGroups, filters.resourceId, systemAccountId, isManagementView.value)
        },
        query: { purpose: 'filter-resource', resourceType, systemAccountId, requestKeyword, selectedId: filters.resourceId, limit: remoteOptionLimit },
        route: resourceType === 'account' ? '/accounts/options' : '/groups/options',
        targetSystemAccountId: systemAccountId
      })
    } catch (error) {
      if (requestId !== filterResourceRequestId) return
      console.error(error)
      message.error(filters.resourceType === 'account' ? '加载可授权账户失败' : '加载可授权分组失败')
    } finally {
      if (requestId === filterResourceRequestId) {
        filterResourceOptionsLoading.value = false
      }
    }
  }

  async function loadFilterTeamOptions(keyword?: string): Promise<void> {
    if (!isManagementView.value) {
      teams.value = []
      return
    }
    const requestKeyword = normalizeSearchKeyword(keyword)
    const requestId = ++filterTeamRequestId
    filterTeamOptionsLoading.value = true
    try {
      await loadAuthorizationOptionResource<SystemTeamPrincipalSummary[]>({
        apply: (nextTeams) => {
          syncFilterTeamSelection(nextTeams)
          teams.value = nextTeams
        },
        domain: 'teams.options',
        isCurrent: () => requestId === filterTeamRequestId,
        isManagementView: true,
        loadNetwork: async () => {
          const nextTeams = await api.authorizationOptions.granteeTeams({ keyword: requestKeyword, limit: remoteOptionLimit })
          return await ensureSelectedTeamOption(nextTeams, filters.teamId, isManagementView.value)
        },
        query: { purpose: 'filter-team', requestKeyword, selectedId: filters.teamId, limit: remoteOptionLimit },
        route: '/authorization-options/grantee-teams'
      })
    } catch (error) {
      if (requestId !== filterTeamRequestId) return
      console.error(error)
      message.error('加载团队列表失败')
    } finally {
      if (requestId === filterTeamRequestId) {
        filterTeamOptionsLoading.value = false
      }
    }
  }

  async function loadFilterUserOptions(keyword?: string): Promise<void> {
    if (!isManagementView.value) {
      users.value = []
      return
    }
    const requestKeyword = normalizeSearchKeyword(keyword)
    const requestId = ++filterUserRequestId
    filterUserOptionsLoading.value = true
    try {
      await loadAuthorizationOptionResource<SystemAccountPrincipalSummary[]>({
        apply: (nextUsers) => {
          syncFilterUserSelection(nextUsers)
          users.value = nextUsers
        },
        domain: 'systemAccounts.options',
        isCurrent: () => requestId === filterUserRequestId,
        isManagementView: true,
        loadNetwork: async () => {
          const nextUsers = await api.authorizationOptions.granteeAccounts({ keyword: requestKeyword, limit: remoteOptionLimit })
          return await ensureSelectedSystemAccountPrincipal(nextUsers, filters.granteeSystemAccountId, isManagementView.value)
        },
        query: { purpose: 'filter-user', requestKeyword, selectedId: filters.granteeSystemAccountId, limit: remoteOptionLimit },
        route: '/authorization-options/grantee-accounts'
      })
    } catch (error) {
      if (requestId !== filterUserRequestId) return
      console.error(error)
      message.error('加载系统账户列表失败')
    } finally {
      if (requestId === filterUserRequestId) {
        filterUserOptionsLoading.value = false
      }
    }
  }

  const createOwnerSearch = createAuthorizationSearchScheduler({ delayMs: remoteSearchDelayMs, keyword: createOwnerSearchKeyword, load: loadCreateOwnerOptions })
  const createResourceSearch = createAuthorizationSearchScheduler({ delayMs: remoteSearchDelayMs, keyword: createResourceSearchKeyword, load: loadCreateResourceOptions })
  const createGranteeSearch = createAuthorizationSearchScheduler({ delayMs: remoteSearchDelayMs, keyword: createGranteeSearchKeyword, load: loadCreateGranteeOptions })
  const createTargetGroupSearch = createAuthorizationSearchScheduler({ delayMs: remoteSearchDelayMs, keyword: createTargetGroupSearchKeyword, load: loadCreateTargetGroupOptions })
  const filterResourceSearch = createAuthorizationSearchScheduler({ delayMs: remoteSearchDelayMs, keyword: filterResourceSearchKeyword, load: loadFilterResourceOptions })
  const filterTeamSearch = createAuthorizationSearchScheduler({ delayMs: remoteSearchDelayMs, keyword: filterTeamSearchKeyword, load: loadFilterTeamOptions })
  const filterUserSearch = createAuthorizationSearchScheduler({ delayMs: remoteSearchDelayMs, keyword: filterUserSearchKeyword, load: loadFilterUserOptions })
  const scheduleCreateOwnerSearch = createOwnerSearch.schedule
  const scheduleCreateResourceSearch = createResourceSearch.schedule
  const scheduleCreateGranteeSearch = createGranteeSearch.schedule
  const scheduleCreateTargetGroupSearch = createTargetGroupSearch.schedule
  const scheduleFilterResourceSearch = filterResourceSearch.schedule
  const scheduleFilterTeamSearch = filterTeamSearch.schedule
  const scheduleFilterUserSearch = filterUserSearch.schedule
  const clearCreateOwnerSearchTimer = createOwnerSearch.clear
  const clearCreateResourceSearchTimer = createResourceSearch.clear
  const clearCreateGranteeSearchTimer = createGranteeSearch.clear
  const clearCreateTargetGroupSearchTimer = createTargetGroupSearch.clear
  const clearFilterResourceSearchTimer = filterResourceSearch.clear
  const clearFilterTeamSearchTimer = filterTeamSearch.clear
  const clearFilterUserSearchTimer = filterUserSearch.clear

  function resetCreateOptionSearchState() {
    createOwnerSearchKeyword.value = ''
    createResourceSearchKeyword.value = ''
    createGranteeSearchKeyword.value = ''
    createTargetGroupSearchKeyword.value = ''
    clearCreateOwnerSearchTimer()
    clearCreateResourceSearchTimer()
    clearCreateGranteeSearchTimer()
    clearCreateTargetGroupSearchTimer()
    resetCreateTargetGroupState()
  }

  function resetCreateTargetGroupState() {
    createForm.targetGroupId = ''
    createForm.targetGroup = undefined
    createTargetGroups.value = []
    createTargetGroupSearchKeyword.value = ''
    clearCreateTargetGroupSearchTimer()
  }

  function resetFilterResource() {
    filters.resourceId = undefined
    filters.resourceAccount = undefined
    filters.resourceGroup = undefined
  }

  function resetFilterResourceOptions() {
    accounts.value = []
    groups.value = []
  }

  function resetFilterOptionLists() {
    resetFilterResourceOptions()
    teams.value = []
    users.value = []
  }

  function resetFilterOptionSearchState() {
    filterResourceSearchKeyword.value = ''
    filterTeamSearchKeyword.value = ''
    filterUserSearchKeyword.value = ''
    clearFilterResourceSearchTimer()
    clearFilterTeamSearchTimer()
    clearFilterUserSearchTimer()
  }

  function syncCreateResourceGroup(nextGroups = createGroups.value): void {
    if (createForm.resourceType !== 'group') {
      createForm.resourceGroup = undefined
      return
    }
    createForm.resourceAccount = undefined
    createForm.resourceGroup = selectedGroupFromOptions(createForm.resourceId, nextGroups, createForm.resourceGroup)
  }

  function syncFilterResourceGroup(nextGroups = groups.value): void {
    if (filters.resourceType !== 'group') {
      filters.resourceGroup = undefined
      return
    }
    filters.resourceAccount = undefined
    filters.resourceGroup = selectedGroupFromOptions(filters.resourceId, nextGroups, filters.resourceGroup)
  }

  function syncCreateResourceAccount(nextAccounts = createAccounts.value): void {
    if (createForm.resourceType !== 'account') {
      createForm.resourceAccount = undefined
      return
    }
    createForm.resourceGroup = undefined
    createForm.resourceAccount = selectedAccountFromOptions(createForm.resourceId, nextAccounts, createForm.resourceAccount)
    rememberAccountSelection(createForm.resourceAccount)
  }

  function syncCreateTargetGroup(nextGroups = createTargetGroups.value): void {
    if (!createTargetGroupVisible.value) {
      createForm.targetGroup = undefined
      return
    }
    createForm.targetGroup = selectedGroupFromOptions(createForm.targetGroupId, nextGroups, createForm.targetGroup)
  }

  function selectDefaultCreateTargetGroup(nextGroups = createTargetGroups.value): void {
    if (createForm.targetGroupId || createTargetGroupSearchKeyword.value.trim()) return
    const defaultGroup = nextGroups.find((group) => group.enabled && group.isDefault)
    if (!defaultGroup) return
    createForm.targetGroupId = defaultGroup.id
    createForm.targetGroup = { id: defaultGroup.id, name: defaultGroup.name }
  }

  function syncFilterResourceAccount(nextAccounts = accounts.value): void {
    if (filters.resourceType !== 'account') {
      filters.resourceAccount = undefined
      return
    }
    filters.resourceGroup = undefined
    filters.resourceAccount = selectedAccountFromOptions(filters.resourceId, nextAccounts, filters.resourceAccount)
    rememberAccountSelection(filters.resourceAccount)
  }

  function syncFilterTeamSelection(nextTeams = teams.value): void {
    filters.team = selectedTeamFromOptions(filters.teamId, nextTeams, filters.team)
  }

  function syncFilterUserSelection(nextUsers = users.value): void {
    filters.granteeSystemAccount = selectedUserFromOptions(filters.granteeSystemAccountId, nextUsers, filters.granteeSystemAccount)
  }

  return {
    accounts,
    groups,
    createAccounts,
    createGroups,
    createTargetGroups,
    teams,
    users,
    createOwnerUsers,
    createUsers,
    createTeams,
    createOwnerUsersLoading,
    createResourceOptionsLoading,
    createGranteeOptionsLoading,
    createTargetGroupOptionsLoading,
    filterResourceOptionsLoading,
    filterTeamOptionsLoading,
    filterUserOptionsLoading,
    createOwnerSearchKeyword,
    createResourceSearchKeyword,
    createGranteeSearchKeyword,
    createTargetGroupSearchKeyword,
    filterResourceSearchKeyword,
    filterTeamSearchKeyword,
    filterUserSearchKeyword,
    filterResourceDisabled,
    createOwnedAccounts,
    createOwnedGroups,
    selectedCreateAccount,
    createTargetGroupVisible,
    loadCreateOwnerOptions,
    loadCreateResourceOptions,
    loadCreateGranteeOptions,
    loadCreateTargetGroupOptions,
    loadFilterResourceOptions,
    loadFilterTeamOptions,
    loadFilterUserOptions,
    scheduleCreateOwnerSearch,
    scheduleCreateResourceSearch,
    scheduleCreateGranteeSearch,
    scheduleCreateTargetGroupSearch,
    scheduleFilterResourceSearch,
    scheduleFilterTeamSearch,
    scheduleFilterUserSearch,
    clearCreateResourceSearchTimer,
    clearCreateGranteeSearchTimer,
    clearFilterResourceSearchTimer,
    resetCreateOptionSearchState,
    resetCreateTargetGroupState,
    resetFilterResource,
    resetFilterResourceOptions,
    resetFilterOptionLists,
    resetFilterOptionSearchState
  }
}
