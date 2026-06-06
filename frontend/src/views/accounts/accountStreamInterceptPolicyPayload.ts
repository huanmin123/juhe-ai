import type {
  AccountStreamInterceptRuleForm,
  AccountStreamInterceptRulePayload,
  AccountStreamInterceptValidationResult
} from './accountStreamInterceptPolicyTypes'
import { createBlankAccountStreamInterceptRule } from './accountStreamInterceptPolicyOptions'
import type { StreamInterceptPolicyAction } from '@/types/domain'

const listSeparators = /[,;，；\n]/

export function loadAccountStreamInterceptRules(credentials?: Record<string, unknown>): AccountStreamInterceptRuleForm[] {
  if (!credentials || !Object.prototype.hasOwnProperty.call(credentials, 'stream_intercept_rules')) {
    return []
  }
  if (!Array.isArray(credentials.stream_intercept_rules)) {
    throw new Error('账户流式拦截规则必须是数组')
  }
  const source = credentials.stream_intercept_rules
  return source.map(ruleFromPayload)
}

export function validateAccountStreamInterceptRules(rules: AccountStreamInterceptRuleForm[]): AccountStreamInterceptValidationResult {
  if (rules.length > 20) {
    return { valid: false, message: '账户流式拦截规则不能超过 20 条' }
  }
  for (const [index, rule] of rules.entries()) {
    const ruleIndex = index + 1
    if (typeof rule.enabled !== 'boolean') {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则启用状态无效` }
    }
    if (typeof rule.name !== 'string' || !rule.name.trim()) {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则名称不能为空` }
    }
    if (!positiveInt(rule.priority, 9999)) {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则优先级必须是 1-9999 的整数` }
    }
    const action = actionValue(rule.action)
    if (!action) {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则动作无效` }
    }
    const listValidation = validateMatchLists(rule, ruleIndex)
    if (listValidation) return listValidation
    const hasMatcher = [
      rule.eventTypes,
      rule.dataTypes,
      rule.errorCodes,
      rule.errorTypes,
      rule.textIncludes,
      rule.jsonPathsExists
    ].some((value) => splitList(value).length > 0)
    if (rule.enabled && !hasMatcher) {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则至少需要一个匹配条件` }
    }
  }
  return { valid: true }
}

export function writeAccountStreamInterceptRulesToCredentials(credentials: Record<string, unknown>, rules: AccountStreamInterceptRuleForm[]): void {
  const payload = buildAccountStreamInterceptRulePayload(rules)
  if (payload.length > 0) {
    credentials.stream_intercept_rules = payload
  } else {
    delete credentials.stream_intercept_rules
  }
}

export function buildAccountStreamInterceptRulePayload(rules: AccountStreamInterceptRuleForm[]): AccountStreamInterceptRulePayload[] {
  return rules.map((rule) => {
    const action = requiredAction(rule.action)
    const payload: AccountStreamInterceptRulePayload = {
      enabled: booleanValue(rule.enabled, '启用状态'),
      name: requiredString(rule.name, '规则名称', 100),
      priority: requiredPositiveInt(rule.priority, '优先级', 9999),
      match: {},
      action
    }
    addList(payload.match, 'eventTypes', rule.eventTypes, '事件类型')
    addList(payload.match, 'dataTypes', rule.dataTypes, '数据类型')
    addList(payload.match, 'errorCodes', rule.errorCodes, '错误码')
    addList(payload.match, 'errorTypes', rule.errorTypes, '错误类型')
    addList(payload.match, 'textIncludes', rule.textIncludes, '包含文本')
    addList(payload.match, 'textExcludes', rule.textExcludes, '排除文本')
    addList(payload.match, 'jsonPathsExists', rule.jsonPathsExists, 'JSON 路径')
    if (rule.notes.trim()) payload.notes = rule.notes.trim()
    return payload
  })
}

function ruleFromPayload(value: unknown): AccountStreamInterceptRuleForm {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('账户流式拦截规则格式无效')
  const record = value as Record<string, unknown>
  if (!record.match || typeof record.match !== 'object' || Array.isArray(record.match)) {
    throw new Error('账户流式拦截规则匹配条件无效')
  }
  const match = record.match as Record<string, unknown>
  const action = actionValue(record.action)
  if (!action) throw new Error('账户流式拦截规则动作无效')
  const rule: AccountStreamInterceptRuleForm = {
    ...createBlankAccountStreamInterceptRule(requiredPositiveInt(record.priority, '优先级', 9999)),
    enabled: booleanValue(record.enabled, '启用状态'),
    name: requiredString(record.name, '规则名称', 100),
    eventTypes: formatPayloadList(match.eventTypes, '事件类型'),
    dataTypes: formatPayloadList(match.dataTypes, '数据类型'),
    errorCodes: formatPayloadList(match.errorCodes, '错误码'),
    errorTypes: formatPayloadList(match.errorTypes, '错误类型'),
    textIncludes: formatPayloadList(match.textIncludes, '包含文本'),
    textExcludes: formatPayloadList(match.textExcludes, '排除文本'),
    jsonPathsExists: formatPayloadList(match.jsonPathsExists, 'JSON 路径'),
    action,
    notes: stringValue(record.notes)
  }
  return rule
}

function addList(target: AccountStreamInterceptRulePayload['match'], key: keyof AccountStreamInterceptRulePayload['match'], value: string, label: string): void {
  const items = payloadList(value, label)
  if (items.length) {
    target[key] = items
  }
}

function splitList(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.map((item) => String(item).trim()).filter(Boolean)
  }
  if (typeof value !== 'string') {
    return value == null ? [] : [String(value).trim()].filter(Boolean)
  }
  return value.split(listSeparators).map((item) => item.trim()).filter(Boolean)
}

function positiveInt(value: unknown, max = Number.POSITIVE_INFINITY): number | null {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value > 0 && value <= max ? value : null
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function requiredPositiveInt(value: unknown, label: string, max = Number.POSITIVE_INFINITY): number {
  const numberValue = positiveInt(value, max)
  if (numberValue === null) throw new Error(`账户流式拦截规则${label}无效`)
  return numberValue
}

function requiredString(value: unknown, label: string, max = 200): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`账户流式拦截规则${label}无效`)
  const text = value.trim()
  if (text.length > max) throw new Error(`账户流式拦截规则${label}不能超过 ${max} 个字符`)
  return text
}

function booleanValue(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') throw new Error(`账户流式拦截规则${label}无效`)
  return value
}

function formatPayloadList(value: unknown, label: string): string {
  if (value === undefined) return ''
  if (!Array.isArray(value)) throw new Error(`账户流式拦截规则${label}必须是字符串数组`)
  const items = value.map((item) => requiredString(item, label))
  if (items.length > 50) throw new Error(`账户流式拦截规则${label}不能超过 50 项`)
  return items.join(', ')
}

function payloadList(value: unknown, label: string): string[] {
  const items = splitList(value)
  if (items.length > 50) throw new Error(`账户流式拦截规则${label}不能超过 50 项`)
  for (const item of items) {
    requiredString(item, label)
  }
  return items
}

function validateMatchLists(rule: AccountStreamInterceptRuleForm, ruleIndex: number): AccountStreamInterceptValidationResult | undefined {
  const fields: Array<[string, string]> = [
    [rule.eventTypes, '事件类型'],
    [rule.dataTypes, '数据类型'],
    [rule.errorCodes, '错误码'],
    [rule.errorTypes, '错误类型'],
    [rule.textIncludes, '包含文本'],
    [rule.textExcludes, '排除文本'],
    [rule.jsonPathsExists, 'JSON 路径']
  ]
  for (const [value, label] of fields) {
    try {
      payloadList(value, label)
    } catch (error) {
      return { valid: false, index: ruleIndex, message: error instanceof Error ? `第 ${ruleIndex} 条${error.message.replace(/^账户流式拦截规则/, '流式拦截规则')}` : `第 ${ruleIndex} 条流式拦截规则${label}无效` }
    }
  }
  return undefined
}

function requiredAction(value: unknown): StreamInterceptPolicyAction {
  const action = actionValue(value)
  if (!action) throw new Error('账户流式拦截规则动作无效')
  return action
}

function actionValue(value: unknown): StreamInterceptPolicyAction | undefined {
  if (
    value === 'observe'
    || value === 'drop_event'
    || value === 'retry_no_avoidance'
    || value === 'retry_next_account'
    || value === 'avoid_account_ttl'
    || value === 'avoid_upstream_bucket_ttl'
  ) {
    return value
  }
  return undefined
}
