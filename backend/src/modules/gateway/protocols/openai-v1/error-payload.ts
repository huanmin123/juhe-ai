import {
  parseJsonObjectErrorPayload
} from '../_shared/error-payload.js'
import type { GatewayProtocolErrorPayload } from '../_shared/types.js'

export function parseOpenAIErrorPayload(text: string, headers: Headers): GatewayProtocolErrorPayload {
  const parsed = parseJsonObjectErrorPayload(text, headers)
  if (!parsed) return {}
  const { payload, error } = parsed
  return {
    code: error.code ?? payload.code,
    type: error.type ?? payload.type,
    message: error.message ?? payload.message
  }
}
