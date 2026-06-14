import { computed, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { rememberAccountLabels, rememberAccountSelection } from '@/shared/accountLabelCache'
import { rememberGroupLabels } from '@/shared/groupLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountOptionSummary, GroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import type { AuthorizationCreateFormModel } from './authorizationFormModel'
import {
  mergeOptionsById,
  normalizeSearchKeyword,
  selectedAccountFromOptions,
  selectedGroupFromOptions,
  selectedTeamFromOptions,
  selectedUserFromOptions
} from './authorizationOptionHelpers'
import type { AuthorizationFilters } from './authorizationPageState'

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

  const authorizationAccountOptionCache = createShortLivedQueryCache<AccountOptionSummary[]>({ ttlMs: 10_000 })
  const authorizationGroupOptionCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: 10_000 })
  const authorizationUserOptionCache = createShortLivedQueryCache<SystemAccountPrincipalSummary[]>({ ttlMs: 10_000 })
  const authorizationTeamOptionCache = createShortLivedQueryCache<SystemTeamPrincipalSummary[]>({ ttlMs: 10_000 })
  let createOwnerUserRequestId = 0
  let createOwnerUserSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let createOwnerResourceRequestId = 0
  let createResourceSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let createGranteeRequestId = 0
  let createGranteeSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let createTargetGroupRequestId = 0
  let createTargetGroupSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let filterResourceRequestId = 0
  let filterResourceSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let filterTeamRequestId = 0
  let filterTeamSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let filterUserRequestId = 0
  let filterUserSearchTimer: ReturnType<typeof window.setTimeout> | undefined

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
    const requestKey = JSON.stringify(['create-owner', search ?? '', createForm.ownerSystemAccountId ?? ''])
    const cachedUsers = authorizationUserOptionCache.get(requestKey)
    if (cachedUsers) {
      createOwnerUserRequestId += 1
      createOwnerUsersLoading.value = false
      createOwnerUsers.value = cachedUsers
      return
    }
    const requestId = ++createOwnerUserRequestId
    createOwnerUsersLoading.value = true
    try {
      let nextOptions = await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
      nextOptions = await ensureSelectedSystemAccountPrincipal(nextOptions, createForm.ownerSystemAccountId)
      authorizationUserOptionCache.set(requestKey, nextOptions)
      if (requestId !== createOwnerUserRequestId) return
      createOwnerUsers.value = nextOptions
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
    const requestKey = JSON.stringify(['create-resource', createForm.resourceType, isManagementView.value ? 'management' : 'self', ownerSystemAccountId ?? '', search ?? '', createForm.resourceId ?? ''])
    if (createForm.resourceType === 'account') {
      const cachedAccounts = authorizationAccountOptionCache.get(requestKey)
      if (cachedAccounts) {
        createOwnerResourceRequestId += 1
        createResourceOptionsLoading.value = false
        rememberAccountLabels(cachedAccounts)
        syncCreateResourceAccount(cachedAccounts)
        createAccounts.value = cachedAccounts
        createGroups.value = []
        return
      }
    } else {
      const cachedGroups = authorizationGroupOptionCache.get(requestKey)
      if (cachedGroups) {
        createOwnerResourceRequestId += 1
        createResourceOptionsLoading.value = false
        rememberGroupLabels(cachedGroups)
        syncCreateResourceGroup(cachedGroups)
        createGroups.value = cachedGroups
        createAccounts.value = []
        return
      }
    }
    const requestId = ++createOwnerResourceRequestId
    createResourceOptionsLoading.value = true
    try {
      if (createForm.resourceType === 'account') {
        let nextAccounts = isManagementView.value
          ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
          : await api.myAccounts.options({ keyword: search, limit: remoteOptionLimit })
        nextAccounts = await ensureSelectedAccountOption(nextAccounts, createForm.resourceId, ownerSystemAccountId)
        rememberAccountLabels(nextAccounts)
        syncCreateResourceAccount(nextAccounts)
        authorizationAccountOptionCache.set(requestKey, nextAccounts)
        if (requestId !== createOwnerResourceRequestId) return
        createAccounts.value = nextAccounts
        createGroups.value = []
      } else {
        let nextGroups = isManagementView.value
          ? await api.groups.options({ systemAccountId: ownerSystemAccountId, keyword: search, limit: remoteOptionLimit })
          : await api.myGroups.options({ keyword: search, limit: remoteOptionLimit })
        nextGroups = await ensureSelectedGroupOption(nextGroups, createForm.resourceId, ownerSystemAccountId)
        rememberGroupLabels(nextGroups)
        syncCreateResourceGroup(nextGroups)
        authorizationGroupOptionCache.set(requestKey, nextGroups)
        if (requestId !== createOwnerResourceRequestId) return
        createGroups.value = nextGroups
        createAccounts.value = []
      }
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
    const requestKey = JSON.stringify(['create-grantee', createForm.granteeType, isManagementView.value ? 'management' : 'self', search ?? '', createForm.granteeId ?? '', excludedGranteeIds])
    if (createForm.granteeType === 'system_account') {
      const cachedUsers = authorizationUserOptionCache.get(requestKey)
      if (cachedUsers) {
        createGranteeRequestId += 1
        createGranteeOptionsLoading.value = false
        createUsers.value = cachedUsers
        createTeams.value = []
        return
      }
    } else {
      const cachedTeams = authorizationTeamOptionCache.get(requestKey)
      if (cachedTeams) {
        createGranteeRequestId += 1
        createGranteeOptionsLoading.value = false
        createTeams.value = cachedTeams
        createUsers.value = []
        return
      }
    }
    const requestId = ++createGranteeRequestId
    createGranteeOptionsLoading.value = true
    try {
      if (createForm.granteeType === 'system_account') {
        let nextUsers = isManagementView.value
          ? await api.authorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
          : await api.myAuthorizationOptions.granteeAccounts({ keyword: search, limit: remoteOptionLimit })
        nextUsers = nextUsers.filter((user) => !createExcludedGranteeIds.value.includes(user.id))
        nextUsers = await ensureSelectedSystemAccountPrincipal(nextUsers, createForm.granteeId)
        authorizationUserOptionCache.set(requestKey, nextUsers)
        if (requestId !== createGranteeRequestId) return
        createUsers.value = nextUsers
        createTeams.value = []
      } else {
        let nextTeams = isManagementView.value
          ? await api.authorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
          : await api.myAuthorizationOptions.granteeTeams({ keyword: search, limit: remoteOptionLimit })
        nextTeams = await ensureSelectedTeamOption(nextTeams, createForm.granteeId)
        authorizationTeamOptionCache.set(requestKey, nextTeams)
        if (requestId !== createGranteeRequestId) return
        createTeams.value = nextTeams
        createUsers.value = []
      }
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
    const requestKey = JSON.stringify(['create-target-group', isManagementView.value ? 'management' : 'self', granteeSystemAccountId, providerCode, search ?? '', createForm.targetGroupId ?? ''])
    const cachedGroups = authorizationGroupOptionCache.get(requestKey)
    if (cachedGroups) {
      createTargetGroupRequestId += 1
      createTargetGroupOptionsLoading.value = false
      rememberGroupLabels(cachedGroups)
      syncCreateTargetGroup(cachedGroups)
      selectDefaultCreateTargetGroup(cachedGroups)
      createTargetGroups.value = cachedGroups
      return
    }
    const requestId = ++createTargetGroupRequestId
    createTargetGroupOptionsLoading.value = true
    try {
      let nextGroups = isManagementView.value
        ? await api.authorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
        : await api.myAuthorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, keyword: search, limit: remoteOptionLimit, preferDefault: true })
      nextGroups = await ensureSelectedAuthorizationGranteeGroupOption(nextGroups, createForm.targetGroupId, granteeSystemAccountId, providerCode)
      if (requestId !== createTargetGroupRequestId) return
      rememberGroupLabels(nextGroups)
      syncCreateTargetGroup(nextGroups)
      selectDefaultCreateTargetGroup(nextGroups)
      authorizationGroupOptionCache.set(requestKey, nextGroups)
      createTargetGroups.value = nextGroups
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
    const requestKey = JSON.stringify(['filter-resource', filters.resourceType, systemAccountId ?? '', requestKeyword ?? '', filters.resourceId ?? ''])
    if (filters.resourceType === 'account') {
      const cachedAccounts = authorizationAccountOptionCache.get(requestKey)
      if (cachedAccounts) {
        filterResourceRequestId += 1
        filterResourceOptionsLoading.value = false
        rememberAccountLabels(cachedAccounts)
        syncFilterResourceAccount(cachedAccounts)
        accounts.value = cachedAccounts
        groups.value = []
        return
      }
    } else {
      const cachedGroups = authorizationGroupOptionCache.get(requestKey)
      if (cachedGroups) {
        filterResourceRequestId += 1
        filterResourceOptionsLoading.value = false
        rememberGroupLabels(cachedGroups)
        syncFilterResourceGroup(cachedGroups)
        groups.value = cachedGroups
        accounts.value = []
        return
      }
    }
    const requestId = ++filterResourceRequestId
    filterResourceOptionsLoading.value = true
    try {
      if (filters.resourceType === 'account') {
        const nextAccounts = await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
        const mergedAccounts = await ensureSelectedAccountOption(nextAccounts, filters.resourceId, systemAccountId)
        rememberAccountLabels(mergedAccounts)
        syncFilterResourceAccount(mergedAccounts)
        authorizationAccountOptionCache.set(requestKey, mergedAccounts)
        if (requestId !== filterResourceRequestId) return
        accounts.value = mergedAccounts
        groups.value = []
        return
      }
      const nextGroups = await api.groups.options({ systemAccountId, keyword: requestKeyword, limit: remoteOptionLimit })
      const mergedGroups = await ensureSelectedGroupOption(nextGroups, filters.resourceId, systemAccountId)
      rememberGroupLabels(mergedGroups)
      syncFilterResourceGroup(mergedGroups)
      authorizationGroupOptionCache.set(requestKey, mergedGroups)
      if (requestId !== filterResourceRequestId) return
      groups.value = mergedGroups
      accounts.value = []
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
    const requestKey = JSON.stringify(['filter-team', requestKeyword ?? '', filters.teamId ?? ''])
    const cachedTeams = authorizationTeamOptionCache.get(requestKey)
    if (cachedTeams) {
      filterTeamRequestId += 1
      filterTeamOptionsLoading.value = false
      syncFilterTeamSelection(cachedTeams)
      teams.value = cachedTeams
      return
    }
    const requestId = ++filterTeamRequestId
    filterTeamOptionsLoading.value = true
    try {
      const nextTeams = await api.authorizationOptions.granteeTeams({ keyword: requestKeyword, limit: remoteOptionLimit })
      const mergedTeams = await ensureSelectedTeamOption(nextTeams, filters.teamId)
      authorizationTeamOptionCache.set(requestKey, mergedTeams)
      if (requestId !== filterTeamRequestId) return
      syncFilterTeamSelection(mergedTeams)
      teams.value = mergedTeams
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
    const requestKey = JSON.stringify(['filter-user', requestKeyword ?? '', filters.granteeSystemAccountId ?? ''])
    const cachedUsers = authorizationUserOptionCache.get(requestKey)
    if (cachedUsers) {
      filterUserRequestId += 1
      filterUserOptionsLoading.value = false
      syncFilterUserSelection(cachedUsers)
      users.value = cachedUsers
      return
    }
    const requestId = ++filterUserRequestId
    filterUserOptionsLoading.value = true
    try {
      const nextUsers = await api.authorizationOptions.granteeAccounts({ keyword: requestKeyword, limit: remoteOptionLimit })
      const mergedUsers = await ensureSelectedSystemAccountPrincipal(nextUsers, filters.granteeSystemAccountId)
      authorizationUserOptionCache.set(requestKey, mergedUsers)
      if (requestId !== filterUserRequestId) return
      syncFilterUserSelection(mergedUsers)
      users.value = mergedUsers
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

  function scheduleCreateOwnerSearch(value: string) {
    createOwnerSearchKeyword.value = value
    clearCreateOwnerSearchTimer()
    createOwnerUserSearchTimer = window.setTimeout(() => {
      createOwnerUserSearchTimer = undefined
      void loadCreateOwnerOptions(createOwnerSearchKeyword.value)
    }, remoteSearchDelayMs)
  }

  function scheduleCreateResourceSearch(value: string) {
    createResourceSearchKeyword.value = value
    clearCreateResourceSearchTimer()
    createResourceSearchTimer = window.setTimeout(() => {
      createResourceSearchTimer = undefined
      void loadCreateResourceOptions(createResourceSearchKeyword.value)
    }, remoteSearchDelayMs)
  }

  function scheduleCreateGranteeSearch(value: string) {
    createGranteeSearchKeyword.value = value
    clearCreateGranteeSearchTimer()
    createGranteeSearchTimer = window.setTimeout(() => {
      createGranteeSearchTimer = undefined
      void loadCreateGranteeOptions(createGranteeSearchKeyword.value)
    }, remoteSearchDelayMs)
  }

  function scheduleCreateTargetGroupSearch(value: string) {
    createTargetGroupSearchKeyword.value = value
    clearCreateTargetGroupSearchTimer()
    createTargetGroupSearchTimer = window.setTimeout(() => {
      createTargetGroupSearchTimer = undefined
      void loadCreateTargetGroupOptions(createTargetGroupSearchKeyword.value)
    }, remoteSearchDelayMs)
  }

  function scheduleFilterResourceSearch(value: string) {
    filterResourceSearchKeyword.value = value
    clearFilterResourceSearchTimer()
    filterResourceSearchTimer = window.setTimeout(() => {
      filterResourceSearchTimer = undefined
      void loadFilterResourceOptions(filterResourceSearchKeyword.value)
    }, remoteSearchDelayMs)
  }

  function scheduleFilterTeamSearch(value: string) {
    filterTeamSearchKeyword.value = value
    clearFilterTeamSearchTimer()
    filterTeamSearchTimer = window.setTimeout(() => {
      filterTeamSearchTimer = undefined
      void loadFilterTeamOptions(filterTeamSearchKeyword.value)
    }, remoteSearchDelayMs)
  }

  function scheduleFilterUserSearch(value: string) {
    filterUserSearchKeyword.value = value
    clearFilterUserSearchTimer()
    filterUserSearchTimer = window.setTimeout(() => {
      filterUserSearchTimer = undefined
      void loadFilterUserOptions(filterUserSearchKeyword.value)
    }, remoteSearchDelayMs)
  }

  function clearCreateOwnerSearchTimer() {
    if (createOwnerUserSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(createOwnerUserSearchTimer)
      createOwnerUserSearchTimer = undefined
    }
  }

  function clearCreateResourceSearchTimer() {
    if (createResourceSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(createResourceSearchTimer)
      createResourceSearchTimer = undefined
    }
  }

  function clearCreateGranteeSearchTimer() {
    if (createGranteeSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(createGranteeSearchTimer)
      createGranteeSearchTimer = undefined
    }
  }

  function clearCreateTargetGroupSearchTimer() {
    if (createTargetGroupSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(createTargetGroupSearchTimer)
      createTargetGroupSearchTimer = undefined
    }
  }

  function clearFilterResourceSearchTimer() {
    if (filterResourceSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(filterResourceSearchTimer)
      filterResourceSearchTimer = undefined
    }
  }

  function clearFilterTeamSearchTimer() {
    if (filterTeamSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(filterTeamSearchTimer)
      filterTeamSearchTimer = undefined
    }
  }

  function clearFilterUserSearchTimer() {
    if (filterUserSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(filterUserSearchTimer)
      filterUserSearchTimer = undefined
    }
  }

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

  async function ensureSelectedAccountOption(options: AccountOptionSummary[], selectedId?: string, systemAccountId?: string): Promise<AccountOptionSummary[]> {
    const id = selectedId?.trim()
    if (!id || options.some((item) => item.id === id)) return options
    try {
      const selected = isManagementView.value
        ? await api.accounts.options({ systemAccountId, ids: [id], limit: 1 })
        : await api.myAccounts.options({ ids: [id], limit: 1 })
      return mergeOptionsById(selected, options)
    } catch {
      return options
    }
  }

  async function ensureSelectedGroupOption(options: GroupOptionSummary[], selectedId?: string, systemAccountId?: string): Promise<GroupOptionSummary[]> {
    const id = selectedId?.trim()
    if (!id || options.some((item) => item.id === id)) return options
    try {
      const selected = isManagementView.value
        ? await api.groups.options({ systemAccountId, ids: [id], limit: 1 })
        : await api.myGroups.options({ ids: [id], limit: 1 })
      return mergeOptionsById(selected, options)
    } catch {
      return options
    }
  }

  async function ensureSelectedAuthorizationGranteeGroupOption(options: GroupOptionSummary[], selectedId: string | undefined, granteeSystemAccountId: string, providerCode: string): Promise<GroupOptionSummary[]> {
    const id = selectedId?.trim()
    if (!id || options.some((item) => item.id === id)) return options
    try {
      const selected = isManagementView.value
        ? await api.authorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, ids: [id], limit: 1, preferDefault: true })
        : await api.myAuthorizationOptions.granteeGroups({ granteeSystemAccountId, providerCode, ids: [id], limit: 1, preferDefault: true })
      return mergeOptionsById(selected, options)
    } catch {
      return options
    }
  }

  async function ensureSelectedSystemAccountPrincipal(options: SystemAccountPrincipalSummary[], selectedId?: string): Promise<SystemAccountPrincipalSummary[]> {
    const id = selectedId?.trim()
    if (!id || options.some((item) => item.id === id)) return options
    try {
      const selected = isManagementView.value
        ? await api.authorizationOptions.granteeAccounts({ ids: [id], limit: 1 })
        : await api.myAuthorizationOptions.granteeAccounts({ ids: [id], limit: 1 })
      return mergeOptionsById(selected, options)
    } catch {
      return options
    }
  }

  async function ensureSelectedTeamOption(options: SystemTeamPrincipalSummary[], selectedId?: string): Promise<SystemTeamPrincipalSummary[]> {
    const id = selectedId?.trim()
    if (!id || options.some((item) => item.id === id)) return options
    try {
      const selected = isManagementView.value
        ? await api.authorizationOptions.granteeTeams({ ids: [id], limit: 1 })
        : await api.myAuthorizationOptions.granteeTeams({ ids: [id], limit: 1 })
      return mergeOptionsById(selected, options)
    } catch {
      return options
    }
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
