import {
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
    code: stringErrorField(error.code) ?? stringErrorField(payload.code) ?? type,
    type,
    message: error.message ?? payload.message
  }
}
