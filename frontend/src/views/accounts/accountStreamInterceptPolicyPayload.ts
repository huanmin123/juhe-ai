import type {
  AccountStreamInterceptRuleForm,
  AccountStreamInterceptRulePayload,
  AccountStreamInterceptValidationResult
} from './accountStreamInterceptPolicyTypes'
import { createBlankAccountStreamInterceptRule } from './accountStreamInterceptPolicyOptions'
import {
  defaultAvoidanceTtlSeconds,
  streamInterceptActionUsesTtl
} from '../stream-intercept-policies/streamInterceptActionTemplates'
import type { StreamInterceptPolicyAction } from '@/types/domain'

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
    if (streamInterceptActionUsesTtl(rule.action) && !positiveInt(rule.avoidanceTtlSeconds)) {
      return { valid: false, index: ruleIndex, message: `第 ${ruleIndex} 条流式拦截规则需要配置避让秒数` }
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
      match: {},
      action: rule.action
    }
    addList(payload.match, 'eventTypes', rule.eventTypes)
    addList(payload.match, 'dataTypes', rule.dataTypes)
    addList(payload.match, 'errorCodes', rule.errorCodes)
    addList(payload.match, 'errorTypes', rule.errorTypes)
    addList(payload.match, 'textIncludes', rule.textIncludes)
    addList(payload.match, 'textExcludes', rule.textExcludes)
    addList(payload.match, 'jsonPathsExists', rule.jsonPathsExists)
    const ttl = positiveInt(rule.avoidanceTtlSeconds)
    if (streamInterceptActionUsesTtl(rule.action) && ttl) payload.avoidanceTtlSeconds = ttl
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
  const rule: AccountStreamInterceptRuleForm = {
    ...createBlankAccountStreamInterceptRule(positiveInt(record.priority) ?? (index + 1) * 10),
    enabled: record.enabled !== false,
    name: stringValue(record.name) || '账户流式拦截规则',
    eventTypes: formatList(match.eventTypes),
    dataTypes: formatList(match.dataTypes),
    errorCodes: formatList(match.errorCodes),
    errorTypes: formatList(match.errorTypes),
    textIncludes: formatList(match.textIncludes),
    textExcludes: formatList(match.textExcludes),
    jsonPathsExists: formatList(match.jsonPathsExists),
    action: actionValue(record.action) ?? 'avoid_account_ttl',
    avoidanceTtlSeconds: positiveInt(record.avoidanceTtlSeconds) ?? defaultAvoidanceTtlSeconds,
    notes: stringValue(record.notes)
  }
  if (!streamInterceptActionUsesTtl(rule.action)) {
    rule.avoidanceTtlSeconds = null
  }
  return rule
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

function actionValue(value: unknown): StreamInterceptPolicyAction | undefined {
  if (
    value === 'observe'
    || value === 'drop_event'
    || value === 'fail_stream'
    || value === 'retry_no_avoidance'
    || value === 'retry_next_account'
    || value === 'avoid_account_ttl'
    || value === 'avoid_upstream_bucket_ttl'
  ) {
    return value
  }
  return undefined
}
