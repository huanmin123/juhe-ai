import type { Request } from 'express'

import type { RequestQuotaLimits } from '../../../domain/types.js'
import { estimateProviderCostUsd } from '../../model-pricing/model-pricing.service.js'
import type { GatewayApiKeyRow } from '../../../storage/repositories.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from '../../../storage/request-quota-limits.js'
import type { RequestQuotaCosts } from './request-quota-checker.js'
import { getGatewayRequestBodyState } from '../request/body.js'
import { requestModel } from '../request/metadata.js'
import { readGatewayApiKeyQuotaCostsSnapshotAsync } from './api-key-quota.service.js'
import { getRequestLogger } from '../../../shared/request-context.js'

const defaultEstimatedOutputTokens = 4096
const defaultReleaseDelayMs = 65_000
const reservationLeakTtlMs = 30 * 60_000

interface InflightQuotaState {
  reservedCostUsd: number
}

export interface ApiKeyInflightQuotaReservation {
  complete: () => void
}

export type ApiKeyInflightQuotaDecision =
  | { allowed: true; estimatedCostUsd?: number; reservation?: ApiKeyInflightQuotaReservation }
  | { allowed: false; estimatedCostUsd: number }

const states = new Map<string, InflightQuotaState>()

export async function reserveGatewayApiKeyInflightCost(input: {
  req: Request
  apiKey: GatewayApiKeyRow
  providerCode: string
}): Promise<ApiKeyInflightQuotaDecision> {
  const limits = parseRequestQuotaLimitsJson(input.apiKey.quota_limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) return { allowed: true }
  const estimatedCostUsd = estimateGatewayRequestCostUsd(input.req, input.providerCode)
  if (estimatedCostUsd === undefined || estimatedCostUsd <= 0) return { allowed: true }
  let currentCosts = await readGatewayApiKeyQuotaCostsSnapshotAsync(input.apiKey)
  if (!currentCosts) {
    try {
      const dbService = await import('../../db-service/db-service-ipc.js')
      currentCosts = await dbService.requestDbService({
        type: 'read_api_key_quota_costs',
        apiKey: input.apiKey
      }, { timeoutMs: 1000 })
    } catch (error) {
      getRequestLogger().warn({
        event: 'gateway_api_key_inflight_quota_exact_cost_failed',
        apiKeyId: input.apiKey.id,
        errorMessage: error instanceof Error ? error.message : String(error)
      }, 'API Key 在途额度缺少成本快照且精确成本读取失败，按保护策略拒绝请求')
      return { allowed: false, estimatedCostUsd }
    }
  }
  return reserveApiKeyInflightCost({
    apiKeyId: input.apiKey.id,
    limits,
    currentCosts,
    estimatedCostUsd
  })
}

export function reserveApiKeyInflightCost(input: {
  apiKeyId: string
  limits: RequestQuotaLimits
  currentCosts: RequestQuotaCosts
  estimatedCostUsd: number
  releaseDelayMs?: number
}): ApiKeyInflightQuotaDecision {
  const estimatedCostUsd = normalizedCost(input.estimatedCostUsd)
  if (estimatedCostUsd <= 0) return { allowed: true }
  const state = states.get(input.apiKeyId) ?? { reservedCostUsd: 0 }
  const projected = addCostToAllWindows(input.currentCosts, state.reservedCostUsd + estimatedCostUsd)
  if (isProjectedRequestQuotaExceeded(input.limits, projected)) {
    return { allowed: false, estimatedCostUsd }
  }
  state.reservedCostUsd = normalizedCost(state.reservedCostUsd + estimatedCostUsd)
  states.set(input.apiKeyId, state)
  return {
    allowed: true,
    estimatedCostUsd,
    reservation: createReservation(input.apiKeyId, estimatedCostUsd, input.releaseDelayMs ?? defaultReleaseDelayMs)
  }
}

function isProjectedRequestQuotaExceeded(limits: RequestQuotaLimits, costs: RequestQuotaCosts): boolean {
  return Boolean(
    (limits.hourly?.enabled && costs.hourly > limits.hourly.limit)
    || (limits.daily?.enabled && costs.daily > limits.daily.limit)
    || (limits.weekly?.enabled && costs.weekly > limits.weekly.limit)
    || (limits.monthly?.enabled && costs.monthly > limits.monthly.limit)
    || (limits.total?.enabled && costs.total > limits.total.limit)
  )
}

export function estimateGatewayRequestCostUsd(req: Request, providerCode: string): number | undefined {
  const bodyState = getGatewayRequestBodyState(req)
  const rawBodyBytes = bodyState?.rawBodyBytes ?? Buffer.byteLength(JSON.stringify(req.body ?? {}))
  return estimateProviderCostUsd({
    providerCode,
    model: requestModel(req),
    serviceTier: bodyState?.serviceTier ?? 'default',
    inputTokens: Math.max(1, Math.ceil(rawBodyBytes / 4)),
    outputTokens: bodyState?.maxOutputTokens ?? defaultEstimatedOutputTokens
  })
}

export function apiKeyInflightQuotaSnapshot(): Array<{ apiKeyId: string; reservedCostUsd: number }> {
  return [...states.entries()].map(([apiKeyId, state]) => ({ apiKeyId, reservedCostUsd: state.reservedCostUsd }))
}

export function clearApiKeyInflightQuotaReservationsForTest(): void {
  states.clear()
}

function createReservation(apiKeyId: string, costUsd: number, releaseDelayMs: number): ApiKeyInflightQuotaReservation {
  let completed = false
  let released = false
  const release = () => {
    if (released) return
    released = true
    const state = states.get(apiKeyId)
    if (!state) return
    state.reservedCostUsd = normalizedCost(Math.max(0, state.reservedCostUsd - costUsd))
    if (state.reservedCostUsd === 0) states.delete(apiKeyId)
  }
  const leakTimer = setTimeout(release, reservationLeakTtlMs)
  leakTimer.unref()
  return {
    complete: () => {
      if (completed) return
      completed = true
      clearTimeout(leakTimer)
      const timer = setTimeout(release, Math.max(0, Math.trunc(releaseDelayMs)))
      timer.unref()
    }
  }
}

function addCostToAllWindows(costs: RequestQuotaCosts, amount: number): RequestQuotaCosts {
  return {
    hourly: costs.hourly + amount,
    daily: costs.daily + amount,
    weekly: costs.weekly + amount,
    monthly: costs.monthly + amount,
    total: costs.total + amount
  }
}

function normalizedCost(value: number): number {
  return Number.isFinite(value) ? Number(Math.max(0, value).toFixed(10)) : 0
}
