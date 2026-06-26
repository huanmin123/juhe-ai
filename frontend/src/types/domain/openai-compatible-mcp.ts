import type {
  OpenAICompatibleMcpApprovalPolicy,
  OpenAICompatibleMcpApprovalStatus,
  OpenAICompatibleMcpExecutionStatus
} from './base'

export interface OpenAICompatibleMcpServerSummary {
  id: string
  systemAccountId: string
  label: string
  serverUrl: string
  description?: string
  enabled: boolean
  allowedTools: string[]
  defaultApprovalPolicy: OpenAICompatibleMcpApprovalPolicy
  timeoutMs?: number
  maxRetries?: number
  retryDelayMs?: number
  maxBodyBytes?: number
  maxOutputBytes?: number
  allowRequestAuthorization: boolean
  authorizationRef?: string
  createdAt: string
  updatedAt: string
}

export interface OpenAICompatibleMcpServerListResult {
  items: OpenAICompatibleMcpServerSummary[]
  total: number
  page: number
  pageSize: number
}

export interface OpenAICompatibleMcpToolCacheSummary {
  id: string
  serverId: string
  systemAccountId: string
  serverLabel: string
  serverUrl: string
  toolName: string
  description?: string
  inputSchema: Record<string, unknown>
  annotations?: unknown
  lastCheckedAt: string
  expiresAt?: string
  createdAt: string
  updatedAt: string
}

export interface OpenAICompatibleMcpServerDiagnosticSummary {
  id: string
  serverId: string
  systemAccountId: string
  serverLabel: string
  serverUrl: string
  status: 'succeeded' | 'failed'
  toolCount: number
  errorCode?: string
  errorMessage?: string
  omissionMetadata?: Record<string, unknown>
  startedAt: string
  finishedAt: string
  durationMs: number
  createdAt: string
  updatedAt: string
}

export interface OpenAICompatibleMcpServerToolsResult {
  server: OpenAICompatibleMcpServerSummary
  latestDiagnostic: OpenAICompatibleMcpServerDiagnosticSummary | null
  tools: OpenAICompatibleMcpToolCacheSummary[]
}

export interface OpenAICompatibleMcpServerDiagnosticResult {
  diagnostic: OpenAICompatibleMcpServerDiagnosticSummary
  tools: OpenAICompatibleMcpToolCacheSummary[]
}

export interface OpenAICompatibleMcpApprovalRequestSummary {
  id: string
  systemAccountId: string
  apiKeyId?: string
  groupId?: string
  serverLabel: string
  serverUrl: string
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  status: OpenAICompatibleMcpApprovalStatus
  traceId?: string
  createdAt: string
  updatedAt: string
  approvedAt?: string
  rejectedAt?: string
  consumedAt?: string
  expiresAt: string
  rejectReason?: string
}

export interface OpenAICompatibleMcpApprovalRequestListResult {
  items: OpenAICompatibleMcpApprovalRequestSummary[]
  total: number
  page: number
  pageSize: number
}

export interface OpenAICompatibleMcpExecutionRecordSummary {
  id: string
  systemAccountId: string
  apiKeyId?: string
  groupId?: string
  traceId: string
  approvalRequestId?: string
  serverLabel: string
  serverUrl: string
  toolName: string
  argumentsDigest: string
  argumentsPreview: string
  outputDigest?: string
  outputBytes: number
  outputTruncated: boolean
  status: OpenAICompatibleMcpExecutionStatus
  errorCode?: string
  errorMessage?: string
  omissionMetadata?: Record<string, unknown>
  startedAt: string
  finishedAt: string
  durationMs: number
  createdAt: string
  updatedAt: string
}

export interface OpenAICompatibleMcpExecutionRecordListResult {
  items: OpenAICompatibleMcpExecutionRecordSummary[]
  total: number
  page: number
  pageSize: number
}
