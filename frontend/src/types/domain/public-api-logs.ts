export type PublicApiLogCaptureStatus = 'complete' | 'truncated' | 'empty' | 'dropped'
export type PublicApiLogResultFilter = 'success' | 'failed' | 'all'

export interface PublicApiLogSummary {
  id: string
  traceId?: string
  sourceRefId?: string
  sourceName?: string
  tokenId?: string
  tokenName?: string
  tokenPrefix?: string
  isTestToken: boolean
  method: string
  path: string
  queryString?: string
  clientIp?: string
  userAgent?: string
  statusCode?: number
  success: boolean
  durationMs?: number
  requestSizeBytes: number
  responseSizeBytes: number
  requestCaptureStatus: PublicApiLogCaptureStatus
  responseCaptureStatus: PublicApiLogCaptureStatus
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt: string
  createdAt: string
}

export interface PublicApiLogDetail extends PublicApiLogSummary {
  requestData: Record<string, unknown>
  responseData: Record<string, unknown>
}

export interface PublicApiLogListResult {
  items: PublicApiLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}
