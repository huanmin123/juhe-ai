import {
  firstErrorFieldText,
  parseJsonObjectErrorPayload,
  stringErrorField
} from '../_shared/error-payload.js'
import type { GatewayProtocolErrorPayload } from '../_shared/types.js'

export function parseAnthropicErrorPayload(text: string, headers: Headers): GatewayProtocolErrorPayload {
  const parsed = parseJsonObjectErrorPayload(text, headers)
  if (!parsed) return {}
  const { payload, error } = parsed
  const type = stringErrorField(error.type) ?? stringErrorField(payload.type)
  return {
    code: firstErrorFieldText(error.code, payload.code) ?? type,
    type,
    message: firstErrorFieldText(error.message, error.msg, error.error_message, error.error_description, error.detail, payload.message, payload.msg, payload.error_message, payload.error_description, payload.detail)
  }
}
