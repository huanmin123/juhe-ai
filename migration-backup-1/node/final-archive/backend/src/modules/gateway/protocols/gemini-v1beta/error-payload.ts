import {
  firstErrorFieldText,
  jsonObjectErrorPayload,
  parseJsonObjectErrorPayload,
  stringErrorField
} from '../_shared/error-payload.js'
import type { GatewayProtocolErrorPayload } from '../_shared/types.js'

export function parseGeminiErrorPayload(text: string, headers: Headers): GatewayProtocolErrorPayload {
  const parsed = parseJsonObjectErrorPayload(text, headers)
  return geminiErrorPayloadFromParsed(parsed)
}

export function parseGeminiErrorPayloadFromJsonValue(value: unknown): GatewayProtocolErrorPayload {
  return geminiErrorPayloadFromParsed(jsonObjectErrorPayload(value))
}

function geminiErrorPayloadFromParsed(parsed: ReturnType<typeof jsonObjectErrorPayload>): GatewayProtocolErrorPayload {
  if (!parsed) return {}
  const { payload, error } = parsed
  const status = stringErrorField(error.status) ?? stringErrorField(payload.status)
  return {
    code: firstErrorFieldText(error.code, payload.code) ?? status,
    type: status,
    message: firstErrorFieldText(error.message, error.msg, error.error_message, error.error_description, error.detail, payload.message, payload.msg, payload.error_message, payload.error_description, payload.detail)
  }
}
