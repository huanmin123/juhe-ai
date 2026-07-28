import type { Ref } from 'vue'

import {
  api,
  type AccountOptionParams,
  type ApiKeyListParams,
  type ModelCheckListParams,
  type ModelCheckScopeParams,
  type ModelCheckStreamOptions,
  type OperationLogListParams,
  type UsageRecordListParams
} from '@/api/client'
import type { ModelCheckRunPayload } from '@/types/domain'

type ApiKeyMutationScopeParams = Parameters<typeof api.apiKeys.create>[1]
type ApiKeyCreatePayload = Parameters<typeof api.apiKeys.create>[0]
type ApiKeyUpdatePayload = Parameters<typeof api.apiKeys.update>[1]
type GroupListParams = Parameters<typeof api.groups.listPage>[0]
type GroupMutationScopeParams = Parameters<typeof api.groups.create>[1]
type GroupMutationPayload = Parameters<typeof api.groups.create>[0]
type GroupOptionParams = Parameters<typeof api.groups.options>[0]
type RouteStrategyListParams = Parameters<typeof api.routeStrategies.list>[0]
type RouteStrategyMutationPayload = Parameters<typeof api.routeStrategies.create>[0]
type RouteStrategyMutationScopeParams = Parameters<typeof api.routeStrategies.create>[1]
type RouteStrategyOptionsParams = Parameters<typeof api.routeStrategies.options>[0]
type SystemTeamListParams = Parameters<typeof api.systemTeams.list>[0]

export function useScopedApiKeysApi(isManagementView: Ref<boolean>) {
  return {
    list: (params?: ApiKeyListParams) => isManagementView.value
      ? api.apiKeys.list(params)
      : api.myApiKeys.list(params),
    create: (payload: ApiKeyCreatePayload, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.create(payload, params)
      : api.myApiKeys.create(payload),
    update: (id: string, payload: ApiKeyUpdatePayload, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.update(id, payload, params)
      : api.myApiKeys.update(id, payload),
    secret: (id: string, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.secret(id, params)
      : api.myApiKeys.secret(id),
    refreshKey: (id: string, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.refreshKey(id, params)
      : api.myApiKeys.refreshKey(id),
    delete: (id: string, params?: ApiKeyMutationScopeParams) => isManagementView.value
      ? api.apiKeys.delete(id, params)
      : api.myApiKeys.delete(id)
  }
}

export function useScopedAccountsApi(isManagementView: Ref<boolean>) {
  return {
    options: (params?: AccountOptionParams) => isManagementView.value
      ? api.accounts.options(params)
      : api.myAccounts.options(params)
  }
}

export function useScopedModelCheckAccountOptionsApi(isManagementView: Ref<boolean>) {
  return {
    options: (params: Parameters<typeof api.modelChecks.accountOptions>[0]) => isManagementView.value
      ? api.modelChecks.accountOptions(params)
      : api.myModelChecks.accountOptions(params)
  }
}

export function useScopedGroupsApi(isManagementView: Ref<boolean>) {
  return {
    listPage: (params?: GroupListParams) => isManagementView.value
      ? api.groups.listPage(params)
      : api.myGroups.listPage(params),
    detail: (id: string, params?: GroupMutationScopeParams) => isManagementView.value
      ? api.groups.detail(id, params)
      : api.myGroups.detail(id),
    options: (params?: GroupOptionParams) => isManagementView.value
      ? api.groups.options(params)
      : api.myGroups.options(params),
    create: (payload: GroupMutationPayload, params?: GroupMutationScopeParams) => isManagementView.value
      ? api.groups.create(payload, params)
      : api.myGroups.create(payload),
    update: (id: string, payload: GroupMutationPayload, params?: GroupMutationScopeParams) => isManagementView.value
      ? api.groups.update(id, payload, params)
      : api.myGroups.update(id, payload),
    returnAuthorization: (id: string, params?: GroupMutationScopeParams) => isManagementView.value
      ? api.groups.returnAuthorization(id, params)
      : api.myGroups.returnAuthorization(id),
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

export function useScopedRouteStrategiesApi(isManagementView: Ref<boolean>) {
  return {
    list: (params?: RouteStrategyListParams) => isManagementView.value
      ? api.routeStrategies.list(params)
      : api.myRouteStrategies.list(params),
    options: (params?: RouteStrategyOptionsParams) => isManagementView.value
      ? api.routeStrategies.options(params)
      : api.myRouteStrategies.options(params),
    detail: (id: string, params?: RouteStrategyMutationScopeParams) => isManagementView.value
      ? api.routeStrategies.detail(id, params)
      : api.myRouteStrategies.detail(id),
    create: (payload: RouteStrategyMutationPayload, params?: RouteStrategyMutationScopeParams) => isManagementView.value
      ? api.routeStrategies.create(payload, params)
      : api.myRouteStrategies.create(payload),
    update: (id: string, payload: RouteStrategyMutationPayload, params?: RouteStrategyMutationScopeParams) => isManagementView.value
      ? api.routeStrategies.update(id, payload, params)
      : api.myRouteStrategies.update(id, payload),
    delete: (id: string, params?: RouteStrategyMutationScopeParams) => isManagementView.value
      ? api.routeStrategies.delete(id, params)
      : api.myRouteStrategies.delete(id)
  }
}

export function useScopedModelChecksApi(isManagementView: Ref<boolean>) {
  return {
    options: (params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.options(params)
      : api.myModelChecks.options(),
    active: (params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.active(params)
      : api.myModelChecks.active(),
    run: (payload: ModelCheckRunPayload, params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.run(payload, params)
      : api.myModelChecks.run(payload),
    runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions, params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.runStream(payload, options, params)
      : api.myModelChecks.runStream(payload, options),
    stop: (params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.stop(params)
      : api.myModelChecks.stop(),
    list: (params?: ModelCheckListParams) => isManagementView.value
      ? api.modelChecks.list(params)
      : api.myModelChecks.list(params),
    detail: (id: string, params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.detail(id, params)
      : api.myModelChecks.detail(id),
    qualityPolicy: (params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.qualityPolicy(params)
      : api.myModelChecks.qualityPolicy(),
    saveQualityPolicy: (payload: Parameters<typeof api.modelChecks.saveQualityPolicy>[0], params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.saveQualityPolicy(payload, params)
      : api.myModelChecks.saveQualityPolicy(payload),
    qualitySchedules: (params?: ModelCheckScopeParams & { page?: number; pageSize?: number }) => isManagementView.value
      ? api.modelChecks.qualitySchedules(params)
      : api.myModelChecks.qualitySchedules(params),
    saveQualitySchedule: (payload: Parameters<typeof api.modelChecks.saveQualitySchedule>[0], params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.saveQualitySchedule(payload, params)
      : api.myModelChecks.saveQualitySchedule(payload),
    deleteQualitySchedule: (id: string, params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.deleteQualitySchedule(id, params)
      : api.myModelChecks.deleteQualitySchedule(id)
  }
}

export function useScopedSystemTeamsApi(isManagementView: Ref<boolean>) {
  return {
    list: (params?: SystemTeamListParams) => isManagementView.value
      ? api.systemTeams.list(params)
      : api.myTeams.list(params),
    detail: (id: string, params?: SystemTeamListParams) => isManagementView.value
      ? api.systemTeams.detail(id, params)
      : api.myTeams.detail(id)
  }
}
