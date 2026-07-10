export interface AccountTestFailureEligibilityInput {
  statusCode?: number
  errorCode?: string
  message?: string
}

const configurationStatusCodes = new Set([400, 404, 405, 409, 415, 422])

const configurationErrorCodes = [
  'invalid_model',
  'model_not_found',
  'model_not_supported',
  'unsupported_model',
  'unsupported_endpoint',
  'unsupported_parameter',
  'invalid_request',
  'invalid_request_error'
]

const modelFailurePatterns = [
  /\bmodel\b.*\b(not found|not available|not supported|unsupported|does not exist|access)\b/i,
  /\b(not found|not available|not supported|unsupported|does not exist)\b.*\bmodel\b/i,
  /模型.*(不存在|不可用|不支持|无权限|未开放)/,
  /(不存在|不可用|不支持|无权限|未开放).*模型/
]

export function accountTestFailureEligibleForAccount(input: AccountTestFailureEligibilityInput): boolean {
  const statusCode = normalizedStatusCode(input.statusCode)
  const errorCode = input.errorCode?.trim().toLowerCase() ?? ''
  const message = input.message?.trim() ?? ''

  if (configurationErrorCodes.some((code) => errorCode === code || errorCode.includes(code))) {
    return false
  }
  if (modelFailurePatterns.some((pattern) => pattern.test(message))) {
    return false
  }
  if (statusCode !== undefined && configurationStatusCodes.has(statusCode)) {
    return false
  }
  return true
}

function normalizedStatusCode(value: number | undefined): number | undefined {
  if (!Number.isFinite(value)) return undefined
  const statusCode = Math.trunc(value as number)
  return statusCode >= 100 && statusCode <= 599 ? statusCode : undefined
}
