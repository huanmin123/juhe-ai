import { gatewayJsonBodyInlineParseMaxBytes } from '../../../gateway/request/body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from '../../../gateway/request/json-parser.js'
import { GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { resolveGptRequestOverrideModelCapabilities } from './request-override-capabilities.js'
import {
  applyGptAccountRequestOverrides,
  assertGptAccountRequestOverrideValues,
  effectiveGptAccountRequestOverrides,
  GptAccountRequestOverrideError,
  hasApplicableGptAccountRequestOverrides,
  readGptAccountRequestOverrides,
  type ApplyGptAccountRequestOverridesInput,
  type GptAccountRequestOverrides,
  type GptRequestOverrideModelCapabilities
} from './request-overrides.js'

export async function applyGptAccountRequestOverridesToBody(
  body: Buffer | string | undefined,
  input: ApplyGptAccountRequestOverridesInput & {
    account: DispatchAccountSecret
    upstreamModel?: string
    signal?: AbortSignal
  }
): Promise<Buffer | string | undefined> {
  const overrides = readGptAccountRequestOverridesForGateway(input.credentials)
  if (!hasApplicableGptAccountRequestOverrides(overrides, input.endpointFamily, input.compact === true)) {
    return body
  }
  const parsed = await parseAccountRequestOverrideBody(body, input.signal)
  const { modelCapabilities, effectiveOverrides } = await normalizeGptRequestOverrideCapabilitiesForGateway({
    ...input,
    overrides,
    upstreamModel: input.upstreamModel ?? requestBodyModel(parsed)
  })
  if (!hasApplicableGptAccountRequestOverrides(effectiveOverrides, input.endpointFamily, input.compact === true)) {
    return body
  }
  let overridden: Record<string, unknown>
  try {
    overridden = applyGptAccountRequestOverrides(parsed, {
      ...input,
      modelCapabilities
    })
  } catch (error) {
    throw normalizeGptAccountRequestOverrideError(error)
  }
  const serialized = JSON.stringify(overridden)
  return typeof body === 'string' ? serialized : Buffer.from(serialized, 'utf8')
}

export async function normalizeGptRequestOverrideCapabilitiesForGateway(
  input: ApplyGptAccountRequestOverridesInput & {
    account: DispatchAccountSecret
    upstreamModel?: string
    overrides?: GptAccountRequestOverrides
  }
): Promise<{
  modelCapabilities: GptRequestOverrideModelCapabilities | undefined
  effectiveOverrides: GptAccountRequestOverrides
}> {
  try {
    const overrides = input.overrides ?? readGptAccountRequestOverrides(input.credentials)
    if (!hasApplicableGptAccountRequestOverrides(overrides, input.endpointFamily, input.compact === true)) {
      return { modelCapabilities: input.modelCapabilities, effectiveOverrides: {} }
    }
    const modelCapabilities = input.modelCapabilities
      ?? await resolveGptRequestOverrideModelCapabilities(input.account, input.upstreamModel)
    return {
      modelCapabilities,
      effectiveOverrides: effectiveGptAccountRequestOverrides(overrides, modelCapabilities)
    }
  } catch (error) {
    throw normalizeGptAccountRequestOverrideError(error)
  }
}

function readGptAccountRequestOverridesForGateway(credentials: Record<string, unknown> | undefined) {
  try {
    const overrides = readGptAccountRequestOverrides(credentials)
    assertGptAccountRequestOverrideValues(overrides)
    return overrides
  } catch (error) {
    throw normalizeGptAccountRequestOverrideError(error)
  }
}

function normalizeGptAccountRequestOverrideError(error: unknown): unknown {
  if (!(error instanceof GptAccountRequestOverrideError)) {
    return error
  }
  return new GatewayRequestValidationError(
    error.message,
    error.code,
    { accountScoped: true }
  )
}

export async function parseAccountRequestOverrideBody(
  body: Buffer | string | undefined,
  signal?: AbortSignal
): Promise<Record<string, unknown>> {
  if (body === undefined) {
    throw invalidAccountRequestOverrideBodyError()
  }
  let parsed: unknown
  try {
    if (typeof body === 'string') {
      parsed = JSON.parse(body) as unknown
    } else {
      parsed = body.length > gatewayJsonBodyInlineParseMaxBytes
        ? await parseGatewayJsonBodyInWorker(body, undefined, signal)
        : JSON.parse(body.toString('utf8')) as unknown
    }
  } catch (error) {
    if (isGatewayJsonWorkerQueueFullError(error)) {
      throw new GatewayRequestValidationError(
        '网关请求解析繁忙，请稍后重试',
        'gateway_json_parser_busy',
        { statusCode: 503, type: 'server_overloaded' }
      )
    }
    throw invalidAccountRequestOverrideBodyError()
  }
  if (!isPlainObject(parsed)) {
    throw invalidAccountRequestOverrideBodyError()
  }
  return parsed
}

function invalidAccountRequestOverrideBodyError(): GatewayRequestValidationError {
  return new GatewayRequestValidationError(
    '账户请求覆盖要求请求体是有效的 JSON 对象',
    'invalid_account_request_override_body'
  )
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function requestBodyModel(body: Record<string, unknown>): string | undefined {
  return typeof body.model === 'string' && body.model.trim() ? body.model.trim() : undefined
}
