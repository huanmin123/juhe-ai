import { computed, ref, type ComputedRef, type Ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { rememberAccountLabels, rememberAccountSelection } from '@/shared/accountLabelCache'
import { rememberGroupLabels } from '@/shared/groupLabelCache'
import type { AccountOptionSummary, AuthorizationGranteeGroupOptionSummary, GroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
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
import { createAuthorizationOptionSingleflight, loadAuthorizationOptionResource } from './authorizationOptionResource'

const remoteOptionLimit = 50
const remoteSearchDelayMs = 250

export interface UseAuthorizationOptionStateOptions {
  authorizationRequestContext: ComputedRef<string>
  createExcludedGranteeIds: ComputedRef<string[]>
  createForm: AuthorizationCreateFormModel
  createModalOpen: Ref<boolean>
  filters: AuthorizationFilters
  isManagementView: ComputedRef<boolean>
  selectedFilterOwnerSystemAccountId: ComputedRef<string | undefined>
}

export function useAuthorizationOptionState(options: UseAuthorizationOptionStateOptions) {
  const {
    authorizationRequestContext,
    createExcludedGranteeIds,
    createForm,
    createModalOpen,
    filters,
    isManagementView,
    selectedFilterOwnerSystemAccountId
  } = options

  const accounts = ref<AccountOptionSummary[]>([])
  const groups = ref<GroupOptionSummary[]>([])
  const createAccounts = ref<AccountOptionSummary[]>([])
  const createGroups = ref<GroupOptionSummary[]>([])
  const createTargetGroups = ref<AuthorizationGranteeGroupOptionSummary[]>([])
  const teams = ref<SystemTeamPrincipalSummary[]>([])
  const users = ref<SystemAccountPrincipalSummary[]>([])
  const createOwnerUsers = ref<SystemAccountPrincipalSummary[]>([])
  const createUsers = ref<SystemAccountPrincipalSummary[]>([])
  const createTeams = ref<SystemTeamPrincipalSummary[]>([])
  const createOwnerUsersLoading = ref(false)
  const createResourceOptionsLoading = ref(false)
  const createGranteeOptionsLoading = ref(false)
  const createTargetGroupOptionsLoading = ref(false)
  const createGranteeOptionsLoaded = ref(false)
  const createTargetGroupOptionsLoaded = ref(false)
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
  const createOptionSingleflight = createAuthorizationOptionSingleflight()

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
    if (!createModalOpen.value) return
    if (!isManagementView.value) {
      resetCreateOwnerOptions()
      return
    }
    const search = normalizeSearchKeyword(keyword)
    const searchKeyword = search ?? ''
    createOwnerSearchKeyword.value = searchKeyword
    const requestContext = authorizationRequestContext.value
    const managementView = isManagementView.value
    const selectedId = createForm.ownerSystemAccountId
    const requestId = ++createOwnerUserRequestId
    const isCurrent = () => requestId === createOwnerUserRequestId
      && createModalOpen.value
      && authorizationRequestContext.value === requestContext
      && isManagementView.value === managementView
      && createForm.ownerSystemAccountId === selectedId
      && createOwnerSearchKeyword.value === searchKeyword
    createOwnerUsersLoading.value = true
    try {
      await loadAuthorizationOptionResource<SystemAccountPrincipalSummary[]>({
        apply: (nextOptions) => { createOwnerUsers.value = nextOptions },
        domain: 'systemAccounts.options',
        isCurrent,
        isManagementView: managementView,
        loadNetwork: () => createOptionSingleflight.run(createOptionRequestKey('owner', requestContext, managementView, {
          search,
          selectedId
        }), async () => {
          let nextOptions = await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
          nextOptions = await ensureSelectedSystemAccountPrincipal(nextOptions, selectedId, managementView)
          return nextOptions
        }),
        query: { purpose: 'create-owner', search, selectedId, limit: remoteOptionLimit },
        route: '/authorization-options/grantee-accounts'
      })
    } catch (error) {
      if (!isCurrent()) return
      console.error(error)
      message.error('加载授权人列表失败')
    } finally {
      if (isCurrent()) {
        createOwnerUsersLoading.value = false
      }
    }
  }

  async function loadCreateResourceOptions(keyword?: string): Promise<void> {
    if (!createModalOpen.value) return
    const ownerSystemAccountId = createForm.ownerSystemAccountId
    if (isManagementView.value && !ownerSystemAccountId) {
      resetCreateResourceOptions()
      return
    }
    const search = normalizeSearchKeyword(keyword)
    const searchKeyword = search ?? ''
    createResourceSearchKeyword.value = searchKeyword
    const resourceType: 'account' | 'group' = createForm.resourceType === 'account' ? 'account' : 'group'
    const selectedId = createForm.resourceId
    const requestContext = authorizationRequestContext.value
    const managementView = isManagementView.value
    const requestId = ++createOwnerResourceRequestId
    const isCurrent = () => requestId === createOwnerResourceRequestId
      && createModalOpen.value
      && authorizationRequestContext.value === requestContext
      && isManagementView.value === managementView
      && createForm.ownerSystemAccountId === ownerSystemAccountId
      && createForm.resourceType === resourceType
      && createForm.resourceId === selectedId
      && createResourceSearchKeyword.value === searchKeyword
    createResourceOptionsLoading.value = true
    try {
      const route = resourceType === 'account'
        ? (managementView ? '/accounts/options' : '/my-accounts/options')
        : (managementView ? '/groups/options' : '/my-groups/options')
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
        isCurrent,
        isManagementView: managementView,
        loadNetwork: () => createOptionSingleflight.run(createOptionRequestKey('resource', requestContext, managementView, {
          ownerSystemAccountId,
          resourceType,
          search,
          selectedId
        }), async () => {
          if (resourceType === 'account') {
            let nextAccounts = managementView
              ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
              : await api.myAccounts.options({ keyword: search, limit: remoteOptionLimit })
            return await ensureSelectedAccountOption(nextAccounts, selectedId, ownerSystemAccountId, managementView)
          }
          let nextGroups = managementView
            ? await api.groups.authorizationOptions({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
            : await api.myGroups.authorizationOptions({ keyword: search, limit: remoteOptionLimit })
          return await ensureSelectedGroupOption(nextGroups, selectedId, ownerSystemAccountId, managementView)
        }),
        query: { purpose: 'create-resource', resourceType, ownerSystemAccountId, search, selectedId, limit: remoteOptionLimit },
        route,
        targetSystemAccountId: ownerSystemAccountId
      })
    } catch (error) {
      if (!isCurrent()) return
      console.error(error)
      message.error('加载授权资源失败')
    } finally {
      if (isCurrent()) {
        createResourceOptionsLoading.value = false
      }
    }
  }

  async function loadCreateGranteeOptions(keyword?: string): Promise<void> {
    if (!createModalOpen.value) return
    const search = normalizeSearchKeyword(keyword)
    const searchKeyword = search ?? ''
    createGranteeSearchKeyword.value = searchKeyword
    const excludedGranteeIds = [...createExcludedGranteeIds.value].sort()
    const granteeType: 'system_account' | 'team' = createForm.granteeType === 'system_account' ? 'system_account' : 'team'
    const selectedId = createForm.granteeId
    const requestContext = authorizationRequestContext.value
    const managementView = isManagementView.value
    const requestId = ++createGranteeRequestId
    const isCurrent = () => requestId === createGranteeRequestId
      && createModalOpen.value
      && authorizationRequestContext.value === requestContext
      && isManagementView.value === managementView
      && createForm.granteeType === granteeType
      && createForm.granteeId === selectedId
      && createGranteeSearchKeyword.value === searchKeyword
      && createExcludedGranteeIds.value.slice().sort().join('\u0000') === excludedGranteeIds.join('\u0000')
    createGranteeOptionsLoading.value = true
    try {
      const route = `/${managementView ? '' : 'my-'}authorization-options/grantee-${granteeType === 'system_account' ? 'accounts' : 'teams'}`
      await loadAuthorizationOptionResource<SystemAccountPrincipalSummary[] | SystemTeamPrincipalSummary[]>({
        apply: (nextOptions) => {
          if (granteeType === 'system_account') {
            createUsers.value = nextOptions as SystemAccountPrincipalSummary[]
            createTeams.value = []
          } else {
            createTeams.value = nextOptions as SystemTeamPrincipalSummary[]
            createUsers.value = []
          }
          createGranteeOptionsLoaded.value = true
        },
        domain: granteeType === 'system_account' ? 'systemAccounts.options' : 'teams.options',
        isCurrent,
        isManagementView: managementView,
        loadNetwork: () => createOptionSingleflight.run(createOptionRequestKey('grantee', requestContext, managementView, {
          excludedGranteeIds,
          granteeType,
          search,
          selectedId
        }), async () => {
          if (granteeType === 'system_account') {
            let nextUsers = managementView
              ? await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
              : await api.myAuthorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
            nextUsers = nextUsers.filter((user) => !excludedGranteeIds.includes(user.id))
            return await ensureSelectedSystemAccountPrincipal(nextUsers, selectedId, managementView)
          }
          let nextTeams = managementView
            ? await api.authorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
            : await api.myAuthorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
          return await ensureSelectedTeamOption(nextTeams, selectedId, managementView)
        }),
        query: { purpose: 'create-grantee', granteeType, search, selectedId, excludedGranteeIds, limit: remoteOptionLimit },
        route
      })
    } catch (error) {
      if (!isCurrent()) return
      console.error(error)
      message.error('加载授权对象失败')
    } finally {
      if (isCurrent()) {
        createGranteeOptionsLoading.value = false
      }
    }
  }

  async function loadCreateTargetGroupOptions(keyword?: string): Promise<void> {
    if (!createModalOpen.value) return
    const granteeSystemAccountId = createForm.granteeType === 'system_account' ? createForm.granteeId : ''
    const providerCode = selectedCreateAccount.value?.providerCode
    if (!createTargetGroupVisible.value || !granteeSystemAccountId || !providerCode) {
      createTargetGroupRequestId += 1
      createTargetGroupOptionsLoading.value = false
      createTargetGroups.value = []
      return
    }
    const search = normalizeSearchKeyword(keyword)
    const searchKeyword = search ?? ''
    createTargetGroupSearchKeyword.value = searchKeyword
    const selectedId = createForm.targetGroupId
    const requestContext = authorizationRequestContext.value
    const managementView = isManagementView.value
    const requestId = ++createTargetGroupRequestId
    const isCurrent = () => requestId === createTargetGroupRequestId
      && createModalOpen.value
      && authorizationRequestContext.value === requestContext
      && isManagementView.value === managementView
      && createForm.granteeId === granteeSystemAccountId
      && createForm.targetGroupId === selectedId
      && selectedCreateAccount.value?.providerCode === providerCode
      && createTargetGroupSearchKeyword.value === searchKeyword
    createTargetGroupOptionsLoading.value = true
    try {
      await loadAuthorizationOptionResource<AuthorizationGranteeGroupOptionSummary[]>({
        apply: (nextGroups) => {
          rememberGroupLabels(nextGroups)
          syncCreateTargetGroup(nextGroups)
          selectDefaultCreateTargetGroup(nextGroups)
          createTargetGroups.value = nextGroups
          createTargetGroupOptionsLoaded.value = true
        },
        domain: 'groups.static',
        isCurrent,
        isManagementView: managementView,
        loadNetwork: () => createOptionSingleflight.run(createOptionRequestKey('target-group', requestContext, managementView, {
          granteeSystemAccountId,
          providerCode,
          search,
          selectedId
        }), async () => {
          let nextGroups = managementView
            ? await api.authorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
            : await api.myAuthorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
          return await ensureSelectedAuthorizationGranteeGroupOption(nextGroups, selectedId, granteeSystemAccountId, providerCode, managementView)
        }),
        query: { purpose: 'create-target-group', granteeSystemAccountId, providerCode, search, selectedId, preferDefault: true, limit: remoteOptionLimit },
        route: `/${managementView ? '' : 'my-'}authorization-options/grantee-groups`,
        targetSystemAccountId: managementView ? granteeSystemAccountId : undefined
      })
    } catch (error) {
      if (!isCurrent()) return
      console.error(error)
      message.error('加载目标分组失败')
    } finally {
      if (isCurrent()) {
        createTargetGroupOptionsLoading.value = false
      }
    }
  }

  async function loadFilterResourceOptions(keyword?: string): Promise<void> {
    if (!isManagementView.value) {
      resetFilterResourceOptions()
      return
    }
    if (filters.resourceType === 'all') {
      resetFilterResourceOptions()
      return
    }
    if (filterResourceDisabled.value) {
      resetFilterResource()
      resetFilterResourceOptions()
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
        isCurrent: () => requestId === filterResourceRequestId
          && isManagementView.value
          && !filterResourceDisabled.value
          && filters.resourceType === resourceType
          && selectedFilterOwnerSystemAccountId.value === systemAccountId,
        isManagementView: true,
        loadNetwork: async () => {
          if (resourceType === 'account') {
            const nextAccounts = await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
            return await ensureSelectedAccountOption(nextAccounts, filters.resourceId, systemAccountId, isManagementView.value)
          }
          const nextGroups = await api.groups.authorizationOptions({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
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
    createOptionSingleflight.invalidate()
    createOwnerSearchKeyword.value = ''
    createResourceSearchKeyword.value = ''
    createGranteeSearchKeyword.value = ''
    createTargetGroupSearchKeyword.value = ''
    clearCreateOwnerSearchTimer()
    clearCreateResourceSearchTimer()
    clearCreateGranteeSearchTimer()
    clearCreateTargetGroupSearchTimer()
    resetCreateOwnerOptions()
    resetCreateResourceOptions()
    resetCreateGranteeOptions()
    resetCreateTargetGroupState()
  }

  function createOptionRequestKey(
    kind: 'owner' | 'resource' | 'grantee' | 'target-group',
    requestContext: string,
    managementView: boolean,
    query: Record<string, unknown>
  ): string {
    return JSON.stringify([kind, requestContext, managementView, query])
  }

  function resetCreateOwnerOptions() {
    createOwnerUserRequestId += 1
    createOwnerUsersLoading.value = false
    createOwnerUsers.value = []
  }

  function resetCreateResourceOptions() {
    createOwnerResourceRequestId += 1
    createResourceOptionsLoading.value = false
    createAccounts.value = []
    createGroups.value = []
  }

  function resetCreateGranteeOptions() {
    createGranteeRequestId += 1
    createGranteeOptionsLoading.value = false
    createGranteeOptionsLoaded.value = false
    createUsers.value = []
    createTeams.value = []
  }

  function resetCreateTargetGroupState() {
    createTargetGroupRequestId += 1
    createTargetGroupOptionsLoading.value = false
    createTargetGroupOptionsLoaded.value = false
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
    filterResourceRequestId += 1
    filterResourceOptionsLoading.value = false
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
    const defaultGroup = nextGroups[0]
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
    createGranteeOptionsLoaded,
    createTargetGroupOptionsLoaded,
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
    resetCreateResourceOptions,
    resetCreateGranteeOptions,
    resetCreateTargetGroupState,
    resetFilterResource,
    resetFilterResourceOptions,
    resetFilterOptionLists,
    resetFilterOptionSearchState
  }
}
