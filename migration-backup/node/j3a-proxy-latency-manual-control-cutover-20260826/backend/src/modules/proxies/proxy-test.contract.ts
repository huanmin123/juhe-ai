import { runtimeConfig } from '../../config/runtime.js'

export const manualProxyTestDeadlineMs = runtimeConfig.background.proxyManualTestDeadlineMs

export type ProxyTestItemStatus = 'passed' | 'warning' | 'failed' | 'unknown'

export interface ProxyTestItem {
  name: string
  status: ProxyTestItemStatus
  httpStatus?: number
  latencyMs?: number
  targetUrl?: string
  message: string
}

export interface ProxyTestReport {
  proxyId: string
  proxyName: string
  score: number
  grade: string
  status: ProxyTestItemStatus
  passedCount: number
  warningCount: number
  failedCount: number
  outboundIp?: string
  outboundRegion?: string
  baseLatencyMs?: number
  testedAt: string
  items: ProxyTestItem[]
  message: string
}
