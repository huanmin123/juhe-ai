export interface AccountErrorPolicyValidationResult {
  valid: boolean
  message?: string
}

const statusListSeparators = /[,;，；\n\/]+/

export function validateAccountErrorHandlingRules(value: unknown): AccountErrorPolicyValidationResult {
  if (value === undefined) return { valid: true }
  if (!Array.isArray(value)) {
    return { valid: false, message: '错误处理策略规则格式无效' }
  }

  for (const [index, item] of value.entries()) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      return { valid: false, message: `第 ${index + 1} 条错误处理策略规则格式无效` }
    }
    const rule = item as Record<string, unknown>
    if (rule.enabled === false) continue
    const statusSpec = accountErrorPolicyStatusSpec(rule)
    const successStatusSpec = explicitSuccessStatusSpec(statusSpec)
    if (successStatusSpec) {
      return {
        valid: false,
        message: `第 ${index + 1} 条规则的状态码不能填写 2xx 成功状态码，例如 200`
      }
    }
    const errorCodeSpec = accountErrorPolicyErrorCodeSpec(rule)
    const successErrorCodeSpec = explicitSuccessStatusSpec(errorCodeSpec)
    if (successErrorCodeSpec) {
      return {
        valid: false,
        message: `第 ${index + 1} 条规则的错误码不能填写 2xx 成功码，例如 200`
      }
    }
  }

  return { valid: true }
}

export function validateAccountCredentialsErrorHandlingRules(credentials: unknown): AccountErrorPolicyValidationResult {
  if (!credentials || typeof credentials !== 'object' || Array.isArray(credentials)) {
    return { valid: true }
  }
  const record = credentials as Record<string, unknown>
  if (!Object.prototype.hasOwnProperty.call(record, 'error_handling_rules')) {
    return { valid: true }
  }
  return validateAccountErrorHandlingRules(record.error_handling_rules)
}

export function accountErrorPolicyValidationMessage(result: AccountErrorPolicyValidationResult): string | undefined {
  return result.valid ? undefined : result.message ?? '错误处理策略配置无效'
}

function accountErrorPolicyStatusSpec(rule: Record<string, unknown>): unknown {
  const match = typeof rule.match === 'object' && rule.match !== null && !Array.isArray(rule.match)
    ? rule.match as Record<string, unknown>
    : {}
  return rule.statusCode ?? rule.status_code ?? rule.statusCodes ?? rule.status_codes ?? match.statusCode ?? match.status_code ?? match.statusCodes ?? match.status_codes
}

function accountErrorPolicyErrorCodeSpec(rule: Record<string, unknown>): unknown {
  const match = typeof rule.match === 'object' && rule.match !== null && !Array.isArray(rule.match)
    ? rule.match as Record<string, unknown>
    : {}
  return rule.errorCode ?? rule.error_code ?? rule.errorCodes ?? rule.error_codes ?? match.errorCode ?? match.error_code ?? match.errorCodes ?? match.error_codes
}

function explicitSuccessStatusSpec(spec: unknown): string | undefined {
  if (Array.isArray(spec)) {
    for (const item of spec) {
      const hit = explicitSuccessStatusSpec(item)
      if (hit) return hit
    }
    return undefined
  }
  if (typeof spec === 'number') {
    return isSuccessStatusCode(spec) ? String(spec) : undefined
  }
  if (typeof spec !== 'string') {
    return undefined
  }
  return spec
    .split(statusListSeparators)
    .map((item) => item.trim())
    .filter(Boolean)
    .find(isExplicitSuccessStatusToken)
}

function isExplicitSuccessStatusToken(token: string): boolean {
  const normalized = token.toLowerCase()
  if (normalized === '2xx') return true
  const exact = normalized.match(/^\d{3}$/)
  if (exact) return isSuccessStatusCode(Number(normalized))
  const range = normalized.match(/^(\d{3})\s*-\s*(\d{3})$/)
  if (!range) return false
  const start = Number(range[1])
  const end = Number(range[2])
  if (!Number.isFinite(start) || !Number.isFinite(end)) return false
  return Math.max(start, 200) <= Math.min(end, 299)
}

function isSuccessStatusCode(code: number): boolean {
  return Number.isInteger(code) && code >= 200 && code <= 299
}
