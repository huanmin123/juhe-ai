import type { Ref } from 'vue'

import {
  api,
  type AccountOptionParams,
  type ApiKeyListParams,
  type ModelCheckListParams,
  type ModelCheckScopeParams,
  type ModelCheckStreamOptions,
  type OpenAICompatibleMcpApprovalRejectPayload,
  type OpenAICompatibleMcpApprovalRequestListParams,
  type OpenAICompatibleMcpExecutionRecordListParams,
  type OpenAICompatibleMcpServerDiagnosePayload,
  type OpenAICompatibleMcpServerListParams,
  type OpenAICompatibleMcpServerPayload,
  type OperationLogListParams,
  type UsageRecordListParams
} from '@/api/client'
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

export function useScopedOpenAICompatibleMcpRuntimeApi(isManagementView: Ref<boolean>) {
  return {
    servers: {
      list: (params?: OpenAICompatibleMcpServerListParams) => isManagementView.value
        ? api.mcpRuntime.servers.list(params)
        : api.myMcpRuntime.servers.list(params),
      detail: (id: string, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.servers.detail(id, params)
        : api.myMcpRuntime.servers.detail(id),
      tools: (id: string, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.servers.tools(id, params)
        : api.myMcpRuntime.servers.tools(id),
      diagnose: (id: string, payload?: OpenAICompatibleMcpServerDiagnosePayload, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.servers.diagnose(id, payload, params)
        : api.myMcpRuntime.servers.diagnose(id, payload),
      create: (payload: OpenAICompatibleMcpServerPayload, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.servers.create(payload, params)
        : api.myMcpRuntime.servers.create(payload),
      update: (id: string, payload: Partial<OpenAICompatibleMcpServerPayload>, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.servers.update(id, payload, params)
        : api.myMcpRuntime.servers.update(id, payload),
      delete: (id: string, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.servers.delete(id, params)
        : api.myMcpRuntime.servers.delete(id)
    },
    approvals: {
      list: (params?: OpenAICompatibleMcpApprovalRequestListParams) => isManagementView.value
        ? api.mcpRuntime.approvals.list(params)
        : api.myMcpRuntime.approvals.list(params),
      detail: (id: string, params?: Pick<OpenAICompatibleMcpApprovalRequestListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.approvals.detail(id, params)
        : api.myMcpRuntime.approvals.detail(id),
      approve: (id: string, params?: Pick<OpenAICompatibleMcpApprovalRequestListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.approvals.approve(id, params)
        : api.myMcpRuntime.approvals.approve(id),
      reject: (id: string, payload: OpenAICompatibleMcpApprovalRejectPayload, params?: Pick<OpenAICompatibleMcpApprovalRequestListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.approvals.reject(id, payload, params)
        : api.myMcpRuntime.approvals.reject(id, payload)
    },
    executions: {
      list: (params?: OpenAICompatibleMcpExecutionRecordListParams) => isManagementView.value
        ? api.mcpRuntime.executions.list(params)
        : api.myMcpRuntime.executions.list(params),
      detail: (id: string, params?: Pick<OpenAICompatibleMcpExecutionRecordListParams, 'systemAccountId'>) => isManagementView.value
        ? api.mcpRuntime.executions.detail(id, params)
        : api.myMcpRuntime.executions.detail(id)
    }
  }
}

export function useScopedModelChecksApi(isManagementView: Ref<boolean>) {
  return {
    options: (params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.options(params)
      : api.myModelChecks.options(),
    run: (payload: ModelCheckRunPayload, params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.run(payload, params)
      : api.myModelChecks.run(payload),
    runStream: (payload: ModelCheckRunPayload, options?: ModelCheckStreamOptions, params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.runStream(payload, options, params)
      : api.myModelChecks.runStream(payload, options),
    list: (params?: ModelCheckListParams) => isManagementView.value
      ? api.modelChecks.list(params)
      : api.myModelChecks.list(params),
    detail: (id: string, params?: ModelCheckScopeParams) => isManagementView.value
      ? api.modelChecks.detail(id, params)
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
