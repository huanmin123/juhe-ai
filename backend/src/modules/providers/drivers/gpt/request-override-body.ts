import { gatewayJsonBodyInlineParseMaxBytes } from '../../../gateway/request/body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from '../../../gateway/request/json-parser.js'
import { GatewayRequestValidationError } from '../../../gateway/request/validation-error.js'
import {
  applyGptAccountRequestOverrides,
  GptAccountRequestOverrideError,
  hasApplicableGptAccountRequestOverrides,
  readGptAccountRequestOverrides,
  type ApplyGptAccountRequestOverridesInput
} from './request-overrides.js'

export async function applyGptAccountRequestOverridesToBody(
  body: Buffer | string | undefined,
  input: ApplyGptAccountRequestOverridesInput & { signal?: AbortSignal }
): Promise<Buffer | string | undefined> {
  const overrides = readGptAccountRequestOverridesForGateway(input.credentials)
  if (!hasApplicableGptAccountRequestOverrides(overrides, input.endpointFamily, input.compact === true)) {
    return body
  }
  const parsed = await parseGptRequestOverrideBody(body, input.signal)
  let overridden: Record<string, unknown>
  try {
    overridden = applyGptAccountRequestOverrides(parsed, input)
  } catch (error) {
    throw normalizeGptAccountRequestOverrideError(error)
  }
  const serialized = JSON.stringify(overridden)
  return typeof body === 'string' ? serialized : Buffer.from(serialized, 'utf8')
}

function readGptAccountRequestOverridesForGateway(credentials: Record<string, unknown> | undefined) {
  try {
    return readGptAccountRequestOverrides(credentials)
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

async function parseGptRequestOverrideBody(
  body: Buffer | string | undefined,
  signal?: AbortSignal
): Promise<Record<string, unknown>> {
  if (body === undefined) {
    throw invalidGptRequestOverrideBodyError()
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
    throw invalidGptRequestOverrideBodyError()
  }
  if (!isPlainObject(parsed)) {
    throw invalidGptRequestOverrideBodyError()
  }
  return parsed
}

function invalidGptRequestOverrideBodyError(): GatewayRequestValidationError {
  return new GatewayRequestValidationError(
    'GPT 账户请求覆盖要求请求体是有效的 JSON 对象',
    'invalid_gpt_request_override_body'
  )
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
