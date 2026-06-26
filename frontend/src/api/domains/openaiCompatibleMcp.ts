import type {
  OpenAICompatibleMcpApprovalRequestListResult,
  OpenAICompatibleMcpApprovalRequestSummary,
  OpenAICompatibleMcpExecutionRecordListResult,
  OpenAICompatibleMcpExecutionRecordSummary,
  OpenAICompatibleMcpServerDiagnosticResult,
  OpenAICompatibleMcpServerListResult,
  OpenAICompatibleMcpServerSummary,
  OpenAICompatibleMcpServerToolsResult
} from '@/types/domain'
import type {
  OpenAICompatibleMcpApprovalRejectPayload,
  OpenAICompatibleMcpApprovalRequestListParams,
  OpenAICompatibleMcpExecutionRecordListParams,
  OpenAICompatibleMcpServerDiagnosePayload,
  OpenAICompatibleMcpServerListParams,
  OpenAICompatibleMcpServerPayload
} from '../contracts'
import { http, unwrap } from '../http'
import {
  openAICompatibleMcpApprovalRequestListParams,
  openAICompatibleMcpExecutionRecordListParams,
  openAICompatibleMcpServerListParams
} from '../params'

function mcpRuntimeApi(prefix: '' | 'my-') {
  const includeSystemAccount = prefix === ''
  return {
    servers: {
      list: (params?: OpenAICompatibleMcpServerListParams) =>
        unwrap<OpenAICompatibleMcpServerListResult>(http.get(`/${prefix}mcp-servers`, {
          params: openAICompatibleMcpServerListParams(params, includeSystemAccount)
        })),
      detail: (id: string, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpServerSummary>(http.get(`/${prefix}mcp-servers/${id}`, {
          params: openAICompatibleMcpServerListParams(params, includeSystemAccount)
        })),
      tools: (id: string, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpServerToolsResult>(http.get(`/${prefix}mcp-servers/${id}/tools`, {
          params: openAICompatibleMcpServerListParams(params, includeSystemAccount)
        })),
      diagnose: (id: string, payload?: OpenAICompatibleMcpServerDiagnosePayload, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpServerDiagnosticResult>(http.post(`/${prefix}mcp-servers/${id}/diagnose`, payload ?? {}, {
          params: openAICompatibleMcpServerListParams(params, includeSystemAccount)
        })),
      create: (payload: OpenAICompatibleMcpServerPayload, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpServerSummary>(http.post(`/${prefix}mcp-servers`, payload, {
          params: openAICompatibleMcpServerListParams(params, includeSystemAccount)
        })),
      update: (id: string, payload: Partial<OpenAICompatibleMcpServerPayload>, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpServerSummary>(http.patch(`/${prefix}mcp-servers/${id}`, payload, {
          params: openAICompatibleMcpServerListParams(params, includeSystemAccount)
        })),
      delete: (id: string, params?: Pick<OpenAICompatibleMcpServerListParams, 'systemAccountId'>) =>
        unwrap<boolean>(http.delete(`/${prefix}mcp-servers/${id}`, {
          params: openAICompatibleMcpServerListParams(params, includeSystemAccount)
        }))
    },
    approvals: {
      list: (params?: OpenAICompatibleMcpApprovalRequestListParams) =>
        unwrap<OpenAICompatibleMcpApprovalRequestListResult>(http.get(`/${prefix}mcp-approval-requests`, {
          params: openAICompatibleMcpApprovalRequestListParams(params, includeSystemAccount)
        })),
      detail: (id: string, params?: Pick<OpenAICompatibleMcpApprovalRequestListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpApprovalRequestSummary>(http.get(`/${prefix}mcp-approval-requests/${id}`, {
          params: openAICompatibleMcpApprovalRequestListParams(params, includeSystemAccount)
        })),
      approve: (id: string, params?: Pick<OpenAICompatibleMcpApprovalRequestListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpApprovalRequestSummary>(http.post(`/${prefix}mcp-approval-requests/${id}/approve`, undefined, {
          params: openAICompatibleMcpApprovalRequestListParams(params, includeSystemAccount)
        })),
      reject: (id: string, payload: OpenAICompatibleMcpApprovalRejectPayload, params?: Pick<OpenAICompatibleMcpApprovalRequestListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpApprovalRequestSummary>(http.post(`/${prefix}mcp-approval-requests/${id}/reject`, payload, {
          params: openAICompatibleMcpApprovalRequestListParams(params, includeSystemAccount)
        }))
    },
    executions: {
      list: (params?: OpenAICompatibleMcpExecutionRecordListParams) =>
        unwrap<OpenAICompatibleMcpExecutionRecordListResult>(http.get(`/${prefix}mcp-execution-records`, {
          params: openAICompatibleMcpExecutionRecordListParams(params, includeSystemAccount)
        })),
      detail: (id: string, params?: Pick<OpenAICompatibleMcpExecutionRecordListParams, 'systemAccountId'>) =>
        unwrap<OpenAICompatibleMcpExecutionRecordSummary>(http.get(`/${prefix}mcp-execution-records/${id}`, {
          params: openAICompatibleMcpExecutionRecordListParams(params, includeSystemAccount)
        }))
    }
  }
}

export const openAICompatibleMcpRuntimeApi = mcpRuntimeApi('')
export const myOpenAICompatibleMcpRuntimeApi = mcpRuntimeApi('my-')
