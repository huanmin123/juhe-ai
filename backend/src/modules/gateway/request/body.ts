import type { Request, Response } from 'express'

import type { DbServiceGatewayRuntime } from '../../db-service/db-service-types.js'
import { normalizeUsageReasoningEffort, type UsageReasoningEffort } from '../usage/reasoning-effort.js'
import { normalizeUsageServiceTier, type UsageServiceTier } from '../usage/service-tier.js'
import {
  downgradeAutoImageGenerationToolsInBody,
  inspectImageGenerationTools,
  requestBodyForcesImageGeneration
} from './image-generation-tools.js'
import type { GatewayImageGenerationToolDowngradeResult } from './image-generation-tools.js'

export { requestBodyForcesImageGeneration, requestBodyHasImageGenerationHint } from './image-generation-tools.js'
export type { GatewayImageGenerationToolDowngradeResult } from './image-generation-tools.js'

export const gatewayJsonBodyLargeWarningBytes = 2 * 1024 * 1024
export const gatewayJsonBodyInlineParseMaxBytes = 256 * 1024
export const defaultGatewayTextRawBodyLimitMegabytes = 16
export const gatewayTextRawBodyLimitMegabytesMin = 1
export const gatewayTextRawBodyLimitMegabytesMax = 64
export const defaultGatewayTextRawBodyLimitBytes = defaultGatewayTextRawBodyLimitMegabytes * 1024 * 1024
export const gatewayTextRawBodyHardLimitBytes = defaultGatewayTextRawBodyLimitBytes
export const gatewayTextRawBodyHardLimit = `${defaultGatewayTextRawBodyLimitMegabytes}mb`
export const gatewayImageRawBodyHardLimitBytes = 64 * 1024 * 1024
export const gatewayImageRawBodyHardLimit = '64mb'
export const gatewayRawBodyHardLimitBytes = gatewayImageRawBodyHardLimitBytes
export const gatewayRawBodyHardLimit = gatewayImageRawBodyHardLimit
export const defaultGatewayBodyInFlightMaxBytes = 256 * 1024 * 1024
let gatewayBodyInFlightBytes = 0
let gatewayBodyInFlightRequestCount = 0
let gatewayBodyInFlightRejectedCount = 0
let gatewayBodyInFlightMaxBytesForTest: number | undefined

export type GatewayJsonBodyParseStatus =
  | 'empty'
  | 'not_json'
  | 'parsed'
  | 'deferred_large_json'
  | 'invalid_json'

export interface GatewayRequestBodyState {
  rawBodyBytes: number
  contentType: string
  isJson: boolean
  jsonParseStatus: GatewayJsonBodyParseStatus
  jsonParseWarningBytes: number
  model?: string
  stream?: boolean
  serviceTier?: UsageServiceTier
  reasoningEffort?: UsageReasoningEffort
  maxOutputTokens?: number
  imageGeneration?: boolean
  imageGenerationForced?: boolean
}

export type GatewayRawBodyRequest = Request & {
  rawBody?: Buffer
  gatewayRuntime?: DbServiceGatewayRuntime
  gatewayRequestBody?: GatewayRequestBodyState
  gatewayParsedJsonBodyAvailable?: boolean
  gatewayParsedJsonBody?: unknown
  gatewayParsedJsonBodyPromise?: Promise<unknown>
  gatewayUpstreamBodyCache?: {
    passthrough?: { body: Buffer | undefined }
  }
  gatewayBodyInFlightLease?: GatewayBodyInFlightLease
}

export interface GatewayBodyInFlightState {
  currentBytes: number
  requestCount: number
  maxBytes: number
  rejectedCount: number
}

interface GatewayBodyInFlightLease {
  bytes: number
  release: () => void
}

type ListenerTarget = {
  once?: (event: string, listener: () => void) => unknown
  off?: (event: string, listener: () => void) => unknown
  removeListener?: (event: string, listener: () => void) => unknown
}

export function isGatewayJsonContentType(contentType: unknown): boolean {
  return String(contentType ?? '').toLowerCase().includes('json')
}

export function createGatewayRequestBodyState(input: {
  rawBody: Buffer
  contentType: unknown
  jsonParseStatus: GatewayJsonBodyParseStatus
  parsedBody?: unknown
  model?: string
  stream?: boolean
  serviceTier?: UsageServiceTier
  reasoningEffort?: UsageReasoningEffort
  maxOutputTokens?: number
  imageGeneration?: boolean
  imageGenerationForced?: boolean
}): GatewayRequestBodyState {
  const contentType = String(input.contentType ?? '')
  const parsedBody = typeof input.parsedBody === 'object' && input.parsedBody !== null
    ? input.parsedBody as Record<string, unknown>
    : undefined
  const imageInspection = parsedBody ? inspectImageGenerationTools(parsedBody) : undefined
  return {
    rawBodyBytes: input.rawBody.length,
    contentType,
    isJson: isGatewayJsonContentType(contentType),
    jsonParseStatus: input.jsonParseStatus,
    jsonParseWarningBytes: gatewayJsonBodyLargeWarningBytes,
    model: input.model ?? (typeof parsedBody?.model === 'string' ? parsedBody.model : undefined),
    stream: input.stream ?? (typeof parsedBody?.stream === 'boolean' ? parsedBody.stream : undefined),
    serviceTier: input.serviceTier ?? normalizeUsageServiceTier(parsedBody?.service_tier),
    reasoningEffort: input.reasoningEffort ?? parsedReasoningEffort(parsedBody),
    maxOutputTokens: input.maxOutputTokens ?? parsedMaxOutputTokens(parsedBody),
    imageGeneration: input.imageGeneration ?? (
      imageInspection ? imageInspection.imageToolCount > 0 || imageInspection.forcedImageGeneration : false
    ),
    imageGenerationForced: input.imageGenerationForced ?? imageInspection?.forcedImageGeneration ?? false
  }
}

function parsedReasoningEffort(body: Record<string, unknown> | undefined): UsageReasoningEffort | undefined {
  const nested = typeof body?.reasoning === 'object' && body.reasoning !== null && !Array.isArray(body.reasoning)
    ? (body.reasoning as Record<string, unknown>).effort
    : undefined
  return normalizeUsageReasoningEffort(nested) ?? normalizeUsageReasoningEffort(body?.reasoning_effort)
}

function parsedMaxOutputTokens(body: Record<string, unknown> | undefined): number | undefined {
  if (!body) return undefined
  const values = [body.max_output_tokens, body.max_tokens]
    .filter((value): value is number => Number.isSafeInteger(value) && Number(value) >= 0)
  return values.length > 0 ? Math.max(...values) : undefined
}

export function getGatewayRequestBodyState(req: Request): GatewayRequestBodyState | undefined {
  return (req as GatewayRawBodyRequest).gatewayRequestBody
}

export function tryAcquireGatewayRequestBodyInFlightBytes(
  req: GatewayRawBodyRequest,
  res: Response,
  rawBodyBytes: number,
  configuredMaxBytes?: number
): boolean {
  const bytes = normalizeGatewayBodyInFlightBytes(rawBodyBytes)
  if (bytes <= 0) {
    return true
  }
  releaseGatewayRequestBodyInFlightBytes(req)
  const maxBytes = gatewayRequestBodyInFlightMaxBytes(configuredMaxBytes)
  if (bytes > maxBytes || gatewayBodyInFlightBytes + bytes > maxBytes) {
    gatewayBodyInFlightRejectedCount += 1
    return false
  }

  gatewayBodyInFlightBytes += bytes
  gatewayBodyInFlightRequestCount += 1
  let released = false
  const release = () => {
    if (released) {
      return
    }
    released = true
    gatewayBodyInFlightBytes = Math.max(0, gatewayBodyInFlightBytes - bytes)
    gatewayBodyInFlightRequestCount = Math.max(0, gatewayBodyInFlightRequestCount - 1)
    if (req.gatewayBodyInFlightLease === lease) {
      req.gatewayBodyInFlightLease = undefined
    }
    removeListener(req, 'aborted', release)
    removeListener(res, 'finish', release)
    removeListener(res, 'close', release)
  }
  const lease: GatewayBodyInFlightLease = { bytes, release }
  req.gatewayBodyInFlightLease = lease
  addOneShotListener(req, 'aborted', release)
  addOneShotListener(res, 'finish', release)
  addOneShotListener(res, 'close', release)
  return true
}

export function releaseGatewayRequestBodyInFlightBytes(req: GatewayRawBodyRequest): void {
  req.gatewayBodyInFlightLease?.release()
}

export function getGatewayRequestBodyInFlightState(configuredMaxBytes?: number): GatewayBodyInFlightState {
  return {
    currentBytes: gatewayBodyInFlightBytes,
    requestCount: gatewayBodyInFlightRequestCount,
    maxBytes: gatewayRequestBodyInFlightMaxBytes(configuredMaxBytes),
    rejectedCount: gatewayBodyInFlightRejectedCount
  }
}

export function setGatewayRequestBodyInFlightMaxBytesForTest(value: number | undefined): void {
  gatewayBodyInFlightMaxBytesForTest = typeof value === 'number' && Number.isFinite(value)
    ? Math.max(1, Math.trunc(value))
    : undefined
}

export function gatewayTextRawBodyLimitBytes(configuredMegabytes?: number): number {
  if (typeof configuredMegabytes !== 'number' || !Number.isFinite(configuredMegabytes)) {
    return defaultGatewayTextRawBodyLimitBytes
  }
  const megabytes = Math.trunc(configuredMegabytes)
  if (megabytes < gatewayTextRawBodyLimitMegabytesMin || megabytes > gatewayTextRawBodyLimitMegabytesMax) {
    return defaultGatewayTextRawBodyLimitBytes
  }
  return megabytes * 1024 * 1024
}

export function clearGatewayRequestBodyInFlightForTest(): void {
  gatewayBodyInFlightBytes = 0
  gatewayBodyInFlightRequestCount = 0
  gatewayBodyInFlightRejectedCount = 0
  gatewayBodyInFlightMaxBytesForTest = undefined
}

export function buildGatewayRequestBodySummary(req: Request): Record<string, unknown> | undefined {
  const state = getGatewayRequestBodyState(req)
  if (!state || state.rawBodyBytes <= state.jsonParseWarningBytes) {
    return undefined
  }
  return {
    _gatewayBody: {
      rawBodyBytes: state.rawBodyBytes,
      contentType: state.contentType,
      jsonParseStatus: state.jsonParseStatus,
      jsonParseWarningBytes: state.jsonParseWarningBytes,
      model: state.model ?? (typeof req.body?.model === 'string' ? req.body.model : undefined),
      stream: state.stream ?? (typeof req.body?.stream === 'boolean' ? req.body.stream : undefined),
      imageGeneration: state.imageGeneration,
      imageGenerationForced: state.imageGenerationForced
    }
  }
}

export function gatewayRequestBodyForcesImageGeneration(req: Request): boolean {
  const state = getGatewayRequestBodyState(req)
  return Boolean(state?.imageGenerationForced || requestBodyForcesImageGeneration(req.body))
}

export function downgradeGatewayAutoImageGenerationTool(req: Request): GatewayImageGenerationToolDowngradeResult {
  const result = downgradeAutoImageGenerationToolsInBody(gatewayJsonObjectBody(req))
  if (result.downgraded && result.body) {
    replaceGatewayJsonBody(req, result.body)
  }
  return {
    downgraded: result.downgraded,
    removedToolCount: result.removedToolCount,
    reason: result.reason
  }
}

export function replaceGatewayJsonBodyModel(req: Request, model: string, body?: Record<string, unknown>): boolean {
  const targetModel = model.trim()
  if (!targetModel) {
    return false
  }
  const currentBody = body ?? gatewayJsonObjectBody(req)
  if (!currentBody) {
    return false
  }
  replaceGatewayJsonBody(req, {
    ...currentBody,
    model: targetModel
  })
  return true
}

function gatewayJsonObjectBody(req: Request): Record<string, unknown> | undefined {
  const request = req as GatewayRawBodyRequest
  const body = request.body !== undefined
    ? request.body
    : request.gatewayParsedJsonBodyAvailable
      ? request.gatewayParsedJsonBody
      : undefined
  if (typeof body === 'object' && body !== null && !Array.isArray(body)) {
    return body as Record<string, unknown>
  }
  return undefined
}

export function replaceGatewayJsonBody(req: Request, body: Record<string, unknown>): void {
  const request = req as GatewayRawBodyRequest
  const previousState = getGatewayRequestBodyState(req)
  const contentType = previousState?.contentType ?? String(req.headers['content-type'] ?? 'application/json')
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  request.rawBody = rawBody
  request.body = body
  request.gatewayParsedJsonBodyAvailable = true
  request.gatewayParsedJsonBody = body
  request.gatewayParsedJsonBodyPromise = undefined
  request.gatewayUpstreamBodyCache = undefined
  request.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody,
    contentType,
    jsonParseStatus: previousState?.jsonParseStatus ?? 'parsed',
    parsedBody: body
  })
}

function gatewayRequestBodyInFlightMaxBytes(configuredMaxBytes?: number): number {
  if (gatewayBodyInFlightMaxBytesForTest !== undefined) {
    return gatewayBodyInFlightMaxBytesForTest
  }
  const normalizedMaxBytes = normalizeGatewayBodyInFlightBytes(configuredMaxBytes)
  return normalizedMaxBytes > 0 ? normalizedMaxBytes : defaultGatewayBodyInFlightMaxBytes
}

function normalizeGatewayBodyInFlightBytes(value: unknown): number {
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number) || number <= 0) {
    return 0
  }
  return Math.trunc(number)
}

function addOneShotListener(target: ListenerTarget, event: string, listener: () => void): void {
  if (typeof target.once === 'function') {
    target.once(event, listener)
  }
}

function removeListener(target: ListenerTarget, event: string, listener: () => void): void {
  if (typeof target.off === 'function') {
    target.off(event, listener)
    return
  }
  if (typeof target.removeListener === 'function') {
    target.removeListener(event, listener)
  }
}
