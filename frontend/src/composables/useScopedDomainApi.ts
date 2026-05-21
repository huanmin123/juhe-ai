import type { Ref } from 'vue'

import { api, type AccountListParams, type ApiKeyListParams, type ModelCheckListParams, type OperationLogListParams, type UsageRecordListParams } from '@/api/client'
import type { ModelCheckRunPayload } from '@/types/domain'

type ApiKeyMutationScopeParams = Parameters<typeof api.apiKeys.create>[1]
type ApiKeyMutationPayload = Parameters<typeof api.apiKeys.create>[0]
type GroupListParams = Parameters<typeof api.groups.listPage>[0]
type GroupMutationScopeParams = Parameters<typeof api.groups.create>[1]
type GroupMutationPayload = Parameters<typeof api.groups.create>[0]
type GroupOptionParams = Parameters<typeof api.groups.options>[0]
type SystemTeamListParams = Parameters<typeof api.systemTeams.list>[0]

export function useScopedApiKeysApi(isManagementView: Ref<boolean>) {
  return {
    list: (params?: ApiKeyListParams) => isManagementView.value
      ? api.apiKeys.list(params)
      : api.myApiKeys.list(params),
    create: (payload: ApiKeyMutationPayload, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.create(payload, params)
      : api.myApiKeys.create(payload),
    update: (id: string, payload: ApiKeyMutationPayload, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.update(id, payload, params)
      : api.myApiKeys.update(id, payload),
    delete: (id: string, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.delete(id, params)
      : api.myApiKeys.delete(id)
  }
}

export function useScopedAccountsApi(isManagementView: Ref<boolean>) {
  return {
    options: (params?: AccountListParams) => isManagementView.value
      ? api.accounts.options(params)
      : api.myAccounts.options(params)
  }
}

export function useScopedGroupsApi(isManagementView: Ref<boolean>) {
  return {
    listPage: (params?: GroupListParams) => isManagementView.value
      ? api.groups.listPage(params)
      : api.myGroups.listPage(params),
    options: (params?: GroupOptionParams) => isManagementView.value
      ? api.groups.options(params)
      : api.myGroups.options(params),
    create: (payload: GroupMutationPayload, params?: GroupMutationScopeParams) => isManagementView.value
      ? api.groups.create(payload, params)
      : api.myGroups.create(payload),
    update: (id: string, payload: GroupMutationPayload, params?: GroupMutationScopeParams) => isManagementView.value
      ? api.groups.update(id, payload, params)
      : api.myGroups.update(id, payload),
    delete: (id: string, params?: GroupMutationScopeParams) => isManagementView.value
      ? api.groups.delete(id, params)
      : api.myGroups.delete(id)
  }
}

export function useScopedUsageRecordsApi(isManagementView: Ref<boolean>) {
  return {
    list: (params?: UsageRecordListParams) => isManagementView.value
      ? api.usageRecords.list(params)
      : api.myUsageRecords.list(params)
  }
}

export function useScopedOperationLogsApi(isManagementView: Ref<boolean>) {
  return {
    list: (params?: OperationLogListParams) => isManagementView.value
      ? api.operationLogs.list(params)
      : api.myOperationLogs.list(params),
    detail: (id: string) => isManagementView.value
      ? api.operationLogs.detail(id)
      : api.myOperationLogs.detail(id)
  }
}

export function useScopedModelChecksApi(isManagementView: Ref<boolean>) {
  return {
    options: () => isManagementView.value
      ? api.modelChecks.options()
      : api.myModelChecks.options(),
    run: (payload: ModelCheckRunPayload) => isManagementView.value
      ? api.modelChecks.run(payload)
      : api.myModelChecks.run(payload),
    list: (params?: ModelCheckListParams) => isManagementView.value
      ? api.modelChecks.list(params)
      : api.myModelChecks.list(params),
    detail: (id: string) => isManagementView.value
      ? api.modelChecks.detail(id)
      : api.myModelChecks.detail(id)
  }
}

export function useScopedSystemTeamsApi(isManagementView: Ref<boolean>) {
  return {
    list: (params?: SystemTeamListParams) => isManagementView.value
      ? api.systemTeams.list(params)
      : api.myTeams.list(params)
  }
}
