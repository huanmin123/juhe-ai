import type {
  AccountStreamInterceptRuleForm,
  AccountStreamInterceptRulePayload,
  AccountStreamInterceptValidationResult
} from './accountStreamInterceptPolicyTypes'
import { createBlankAccountStreamInterceptRule } from './accountStreamInterceptPolicyOptions'

const listSeparators = /[,;，；\n]/

export function loadAccountStreamInterceptRules(credentials?: Record<string, unknown>): AccountStreamInterceptRuleForm[] {
  const source = Array.isArray(credentials?.stream_intercept_rules) ? credentials.stream_intercept_rules : []
  return source
    .map((item, index) => ruleFromPayload(item, index))
    .filter((rule): rule is AccountStreamInterceptRuleForm => Boolean(rule))
}

export function validateAccountStreamInterceptRules(rules: AccountStreamInterceptRuleForm[]): AccountStreamInterceptValidationResult {
  if (rules.length > 20) {
    return { valid: false, message: '账户流式拦截规则不能超过 20 条' }
  }
  for (const [index, rule] of rules.entries()) {
    if (rule.enabled === false) continue
    const ruleIndex = index + 1
    const hasMatcher = [
      rule.eventTypes,
      rule.dataTypes,
      rule.errorCodes,
      rule.errorTypes,
      rule.textIncludes,
      rule.jsonPathsExists
    ].some((value) => splitList(value).length > 0)
    if (!hasMatcher) {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则至少需要一个匹配条件` }
    }
    if (rule.retryEnabled && rule.dataHandling === 'discard_event') {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则需要重试时不能只丢弃命中事件` }
    }
    if ((rule.accountSwitch === 'avoid_account_ttl' || rule.accountSwitch === 'avoid_upstream_bucket_ttl' || rule.accountState === 'runtime_avoidance') && !positiveInt(rule.avoidanceTtlSeconds)) {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则需要填写避让秒数` }
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
  return rules.map((rule, index) => {
    const payload: AccountStreamInterceptRulePayload = {
      enabled: rule.enabled !== false,
      name: rule.name.trim() || '账户流式拦截规则',
      priority: positiveInt(rule.priority) ?? (index + 1) * 10,
      executionMode: rule.executionMode,
      match: {},
      dataHandling: rule.dataHandling,
      retryEnabled: rule.retryEnabled,
      accountSwitch: rule.retryEnabled ? rule.accountSwitch : nonRetryAccountSwitch(rule.accountSwitch),
      accountState: rule.accountState
    }
    addList(payload.match, 'eventTypes', rule.eventTypes)
    addList(payload.match, 'dataTypes', rule.dataTypes)
    addList(payload.match, 'errorCodes', rule.errorCodes)
    addList(payload.match, 'errorTypes', rule.errorTypes)
    addList(payload.match, 'textIncludes', rule.textIncludes)
    addList(payload.match, 'textExcludes', rule.textExcludes)
    addList(payload.match, 'jsonPathsExists', rule.jsonPathsExists)
    const ttl = positiveInt(rule.avoidanceTtlSeconds)
    if (ttl) payload.avoidanceTtlSeconds = ttl
    if (rule.notes.trim()) payload.notes = rule.notes.trim()
    return payload
  })
}

function ruleFromPayload(value: unknown, index: number): AccountStreamInterceptRuleForm | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const record = value as Record<string, unknown>
  const match = record.match && typeof record.match === 'object' && !Array.isArray(record.match)
    ? record.match as Record<string, unknown>
    : {}
  return {
    ...createBlankAccountStreamInterceptRule(positiveInt(record.priority) ?? (index + 1) * 10),
    enabled: record.enabled !== false,
    name: stringValue(record.name) || '账户流式拦截规则',
    executionMode: record.executionMode === 'dry_run' ? 'dry_run' : 'intercept',
    eventTypes: formatList(match.eventTypes),
    dataTypes: formatList(match.dataTypes),
    errorCodes: formatList(match.errorCodes),
    errorTypes: formatList(match.errorTypes),
    textIncludes: formatList(match.textIncludes),
    textExcludes: formatList(match.textExcludes),
    jsonPathsExists: formatList(match.jsonPathsExists),
    dataHandling: dataHandling(record.dataHandling),
    retryEnabled: record.retryEnabled === true,
    accountSwitch: accountSwitch(record.accountSwitch),
    accountState: accountState(record.accountState),
    avoidanceTtlSeconds: positiveInt(record.avoidanceTtlSeconds),
    notes: stringValue(record.notes)
  }
}

function addList(target: AccountStreamInterceptRulePayload['match'], key: keyof AccountStreamInterceptRulePayload['match'], value: string): void {
  const items = splitList(value)
  if (items.length) {
    target[key] = items
  }
}

function splitList(value: unknown): string[] {
  if (Array.isArray(value)) {
    return [...new Set(value.map((item) => String(item).trim()).filter(Boolean))].slice(0, 50)
  }
  if (typeof value !== 'string') {
    return value == null ? [] : [String(value).trim()].filter(Boolean)
  }
  return [...new Set(value.split(listSeparators).map((item) => item.trim()).filter(Boolean))].slice(0, 50)
}

function formatList(value: unknown): string {
  return splitList(value).join(', ')
}

function positiveInt(value: unknown): number | null {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue > 0 ? Math.trunc(numberValue) : null
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function dataHandling(value: unknown): AccountStreamInterceptRuleForm['dataHandling'] {
  return value === 'discard_event' || value === 'replace_with_failure' || value === 'discard_stream'
    ? value
    : 'discard_stream'
}

function accountSwitch(value: unknown): AccountStreamInterceptRuleForm['accountSwitch'] {
  return value === 'request_next_account' || value === 'avoid_account_ttl' || value === 'avoid_upstream_bucket_ttl'
    ? value
    : 'none'
}

function accountState(value: unknown): AccountStreamInterceptRuleForm['accountState'] {
  return value === 'runtime_avoidance' ? 'runtime_avoidance' : 'none'
}

function nonRetryAccountSwitch(value: AccountStreamInterceptRuleForm['accountSwitch']): AccountStreamInterceptRuleForm['accountSwitch'] {
  return value === 'request_next_account' ? 'none' : value
}
