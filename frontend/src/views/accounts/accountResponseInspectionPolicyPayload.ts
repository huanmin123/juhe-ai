import {
  makeAccountResponseInspectionRule
} from './accountResponseInspectionPolicyRules'
import type {
  AccountResponseInspectionPolicyValidationResult,
  AccountResponseInspectionRuleForm,
  AccountResponseInspectionRulePayload
} from './accountResponseInspectionPolicyTypes'
import {
  buildResponseInspectionMatchPayload,
  formatResponseInspectionList,
  hasPositiveResponseInspectionMatcher,
  requireResponseInspectionAction,
  responseInspectionActionValues,
  validateResponseInspectionMatchFields
} from '../response-inspection-policies/responseInspectionPolicyForm'

function payloadString(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label}无效`)
  return value.trim()
}

function payloadBoolean(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') throw new Error(`${label}无效`)
  return value
}

function payloadPositiveInt(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 9999) {
    throw new Error(`${label}无效`)
  }
  return value
}

function normalizeOptionalPositiveInt(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value > 0 ? value : null
}

function buildRuleFromPayload(value: unknown): AccountResponseInspectionRuleForm {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('账户响应检查规则格式无效')
  const entry = value as Record<string, unknown>
  const match = entry.match
  if (!match || typeof match !== 'object' || Array.isArray(match)) throw new Error('账户响应检查规则匹配条件无效')
  const record = match as Record<string, unknown>
  return makeAccountResponseInspectionRule({
    enabled: payloadBoolean(entry.enabled, '启用状态'),
    name: payloadString(entry.name, '规则名称'),
    priority: payloadPositiveInt(entry.priority, '优先级'),
    outputTextIncludes: formatResponseInspectionList(payloadStringList(record.outputTextIncludes)),
    outputTextExcludes: formatResponseInspectionList(payloadStringList(record.outputTextExcludes)),
    errorCodes: formatResponseInspectionList(payloadStringList(record.errorCodes)),
    errorTypes: formatResponseInspectionList(payloadStringList(record.errorTypes)),
    errorMessageIncludes: formatResponseInspectionList(payloadStringList(record.errorMessageIncludes)),
    finishReasons: formatResponseInspectionList(payloadStringList(record.finishReasons)),
    jsonPathsExists: formatResponseInspectionList(payloadStringList(record.jsonPathsExists)),
    rawTextIncludes: formatResponseInspectionList(payloadStringList(record.rawTextIncludes)),
    action: requireResponseInspectionAction(entry.action),
    notes: typeof entry.notes === 'string' ? entry.notes.trim() : ''
  })
}

export function loadAccountResponseInspectionRules(credentials?: Record<string, unknown>): AccountResponseInspectionRuleForm[] {
  if (!credentials || !Object.prototype.hasOwnProperty.call(credentials, 'response_inspection_rules')) {
    return []
  }
  if (!Array.isArray(credentials.response_inspection_rules)) {
    throw new Error('账户响应检查规则必须是数组')
  }
  return credentials.response_inspection_rules.map(buildRuleFromPayload)
}

export function validateAccountResponseInspectionRules(rules: AccountResponseInspectionRuleForm[]): AccountResponseInspectionPolicyValidationResult {
  if (rules.length > 20) return { valid: false, message: '账户响应检查规则不能超过 20 条' }
  for (const [index, rule] of rules.entries()) {
    const ruleIndex = index + 1
    if (typeof rule.enabled !== 'boolean') return { valid: false, message: `第 ${ruleIndex} 条响应检查规则启用状态无效`, index: ruleIndex }
    if (typeof rule.name !== 'string' || !rule.name.trim()) return { valid: false, message: `第 ${ruleIndex} 条响应检查规则名称不能为空`, index: ruleIndex }
    if (normalizeOptionalPositiveInt(rule.priority) === null) return { valid: false, message: `第 ${ruleIndex} 条响应检查规则优先级必须是大于 0 的整数`, index: ruleIndex }
    if (!responseInspectionActionValues.includes(rule.action)) return { valid: false, message: `第 ${ruleIndex} 条响应检查规则处置动作无效`, index: ruleIndex }
    const matchValidation = validateResponseInspectionMatchFields(rule, { messagePrefix: `第 ${ruleIndex} 条响应检查规则` })
    if (matchValidation) return { valid: false, message: matchValidation, index: ruleIndex }
    if (rule.enabled && !hasPositiveResponseInspectionMatcher(rule)) {
      return { valid: false, message: `第 ${ruleIndex} 条响应检查规则至少需要一个匹配条件`, index: ruleIndex }
    }
  }
  return { valid: true }
}

export function buildAccountResponseInspectionPayload(rules: AccountResponseInspectionRuleForm[]): AccountResponseInspectionRulePayload[] {
  return rules.map((rule) => {
    const matchValidation = validateResponseInspectionMatchFields(rule)
    if (matchValidation) throw new Error(matchValidation)
    const payload: AccountResponseInspectionRulePayload = {
      enabled: payloadBoolean(rule.enabled, '启用状态'),
      name: payloadString(rule.name, '规则名称'),
      priority: payloadPositiveInt(rule.priority, '优先级'),
      match: buildResponseInspectionMatchPayload(rule),
      action: requireResponseInspectionAction(rule.action)
    }
    const notes = typeof rule.notes === 'string' ? rule.notes.trim() : ''
    if (notes) payload.notes = notes
    return payload
  })
}

export function writeAccountResponseInspectionRulesToCredentials(
  credentials: Record<string, unknown>,
  rules: AccountResponseInspectionRuleForm[]
): void {
  const payload = buildAccountResponseInspectionPayload(rules)
  if (payload.length > 0) {
    credentials.response_inspection_rules = payload
  } else {
    delete credentials.response_inspection_rules
  }
}

function payloadStringList(value: unknown): string[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) throw new Error('账户响应检查规则匹配条件必须是字符串数组')
  return value.map((item) => payloadString(item, '匹配条件'))
}
