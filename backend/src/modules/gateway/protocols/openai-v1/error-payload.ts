import {
  firstErrorFieldText,
  jsonObjectErrorPayload,
  nestedErrorObject,
  parseJsonObjectErrorPayload
} from '../_shared/error-payload.js'
import type { GatewayProtocolErrorPayload } from '../_shared/types.js'

export function parseOpenAIErrorPayload(text: string, headers: Headers): GatewayProtocolErrorPayload {
  const parsed = parseJsonObjectErrorPayload(text, headers)
  return openAIErrorPayloadFromParsed(parsed)
}

export function parseOpenAIErrorPayloadFromJsonValue(value: unknown): GatewayProtocolErrorPayload {
  return openAIErrorPayloadFromParsed(jsonObjectErrorPayload(value))
}

function openAIErrorPayloadFromParsed(parsed: ReturnType<typeof jsonObjectErrorPayload>): GatewayProtocolErrorPayload {
  if (!parsed) return {}
  const { payload, error } = parsed
  const nestedError = nestedErrorObject(error)
  const nestedPayload = nestedErrorObject(payload)
  return {
    code: firstErrorFieldText(error.code, payload.code, nestedError?.code, nestedPayload?.code, error.type, payload.type),
    type: firstErrorFieldText(error.type, payload.type, nestedError?.type, nestedPayload?.type),
    message: firstErrorFieldText(
      error.message,
      error.msg,
      error.error_message,
      error.error_description,
      error.detail,
      error.reason,
      nestedError?.message,
      nestedError?.msg,
      nestedError?.error_message,
      nestedError?.error_description,
      nestedError?.detail,
      nestedPayload?.message,
      nestedPayload?.msg,
      nestedPayload?.error_message,
      nestedPayload?.error_description,
      nestedPayload?.detail,
      payload.message,
      payload.msg,
      payload.error_message,
      payload.error_description,
      payload.detail,
      payload.reason
    )
  }
}
