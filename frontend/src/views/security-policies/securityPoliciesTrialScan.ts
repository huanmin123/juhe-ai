import type { SecurityPolicyRule, SecurityPolicyRuleAction, SecurityPolicyRuleType } from '@/types/domain'
import { securityPolicyDefaultRuleLabel, securityPolicyRuleTypeLabels } from './securityPoliciesForm'

/**
 * 试扫是弹窗内的**本地简化演示**（设计 §4.4：不发请求、不落库）：内置类型用与后端
 * 同语义的简化正则近似（身份证不验校验位、银行卡不验 Luhn、secret 只覆盖常见形态、
 * RE2 用 JS 正则近似编译），mask 保留位数按对称保留近似。展示结果仅供配置参考，
 * 实际命中与替换一律以服务端规则为准。
 */

export interface SecurityPolicyTrialScanHit {
  ruleId: string
  /** 展示标签：规则 label 或按 type 默认词表。 */
  ruleLabel: string
  typeLabel: string
  action: SecurityPolicyRuleAction
  count: number
  /** 替换结果示例（去重后最多 3 个）。 */
  samples: string[]
  /** 本条规则无法本地演示的原因（如 RE2 语法 JS 正则编译失败）；有值时 count=0。 */
  unsupportedReason?: string
}

export interface SecurityPolicyTrialScanResult {
  hits: SecurityPolicyTrialScanHit[]
  /** 全部规则按数组顺序应用后的文本。 */
  output: string
}

/** 内置类型简化演示正则（勿用于业务判定）。 */
const trialScanPatterns: Partial<Record<SecurityPolicyRuleType, RegExp>> = {
  phone: /(?<!\d)1[3-9]\d{9}(?!\d)/g,
  id_card: /(?<!\d)\d{17}[\dXx](?!\d)/g,
  bank_card: /(?<!\d)\d{13,19}(?!\d)/g,
  email: /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g,
  ipv4: /(?<!\d)(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(?:\.(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}(?!\d)/g,
  secret: /(?:sk-[A-Za-z0-9_-]{8,}|AKIA[A-Z0-9]{12,}|Bearer\s+[A-Za-z0-9._~+/=-]{16,}|[A-Za-z0-9_-]{28,})/g
}

/** 内置类型默认保留长度（演示用对称保留近似，契约默认见设计 §3.2）。 */
const trialScanDefaultKeep: Partial<Record<SecurityPolicyRuleType, number>> = {
  phone: 3,
  id_card: 4,
  bank_card: 4,
  email: 1,
  ipv4: 0,
  secret: 4
}

export function trialScanSecurityPolicyText(rules: SecurityPolicyRule[], text: string): SecurityPolicyTrialScanResult {
  const hits: SecurityPolicyTrialScanHit[] = []
  let output = text
  for (const rule of rules) {
    const applied = applyTrialScanRule(rule, output)
    hits.push(applied.hit)
    output = applied.output
  }
  return { hits, output }
}

function applyTrialScanRule(rule: SecurityPolicyRule, text: string): { hit: SecurityPolicyTrialScanHit; output: string } {
  const ruleLabel = rule.label?.trim() || securityPolicyDefaultRuleLabel[rule.type]
  const baseHit: Omit<SecurityPolicyTrialScanHit, 'count' | 'samples' | 'unsupportedReason'> = {
    ruleId: rule.id,
    ruleLabel,
    typeLabel: securityPolicyRuleTypeLabels[rule.type],
    action: rule.action
  }
  let matcher: RegExp | undefined
  let unsupportedReason: string | undefined
  if (rule.type === 'custom_keywords') {
    matcher = undefined
  } else if (rule.type === 'custom_regex') {
    try {
      matcher = new RegExp(rule.pattern?.trim() ?? '', 'g')
    } catch {
      unsupportedReason = '正则无法在本地演示（JS 正则编译失败），实际以服务端 RE2 为准'
    }
  } else {
    matcher = trialScanPatterns[rule.type]
  }

  const replacedByValue = new Map<string, string>()
  let output = text
  let count = 0
  if (rule.type === 'custom_keywords') {
    for (const keyword of rule.keywords ?? []) {
      const trimmed = keyword.trim()
      if (!trimmed) continue
      const occurrences = countLiteralOccurrences(output, trimmed)
      if (!occurrences) continue
      count += occurrences
      output = replaceAllLiteral(output, trimmed, () => trialReplacement(trimmed, rule, ruleLabel, replacedByValue))
    }
  } else if (matcher) {
    output = output.replace(matcher, (matched) => {
      count += 1
      return trialReplacement(matched, rule, ruleLabel, replacedByValue)
    })
  }

  const hit: SecurityPolicyTrialScanHit = {
    ...baseHit,
    count,
    samples: [...replacedByValue.entries()].slice(0, 3).map(([value, replaced]) => `${value} → ${replaced}`),
    ...(unsupportedReason ? { unsupportedReason } : {})
  }
  return { hit, output }
}

function trialReplacement(
  value: string,
  rule: SecurityPolicyRule,
  ruleLabel: string,
  replacedByValue: Map<string, string>
): string {
  const existing = replacedByValue.get(value)
  if (existing) return existing
  // 同一原文片段复用同一编号（契约 §2）；演示按 label 独立计数。
  const replaced = rule.action === 'mask'
    ? trialMaskValue(value, rule)
    : `[[${ruleLabel}_${replacedByValue.size + 1}]]`
  replacedByValue.set(value, replaced)
  return replaced
}

function trialMaskValue(value: string, rule: SecurityPolicyRule): string {
  if (rule.type === 'email') return `${value.slice(0, 1)}***${value.slice(value.indexOf('@'))}`
  if (rule.type === 'ipv4') return `${value.split('.')[0]}.*.*.*`
  const keep = Math.max(0, Math.min(rule.maskKeep ?? trialScanDefaultKeep[rule.type] ?? 0, Math.floor(value.length / 2)))
  const middleLength = Math.max(1, value.length - keep * 2)
  return `${value.slice(0, keep)}${'*'.repeat(Math.min(middleLength, 24))}${keep > 0 ? value.slice(value.length - keep) : ''}`
}

function countLiteralOccurrences(text: string, keyword: string): number {
  if (!keyword) return 0
  let count = 0
  let cursor = 0
  for (;;) {
    const index = text.indexOf(keyword, cursor)
    if (index < 0) break
    count += 1
    cursor = index + keyword.length
  }
  return count
}

function replaceAllLiteral(text: string, keyword: string, replace: () => string): string {
  if (!keyword) return text
  let result = ''
  let cursor = 0
  for (;;) {
    const index = text.indexOf(keyword, cursor)
    if (index < 0) {
      result += text.slice(cursor)
      break
    }
    result += text.slice(cursor, index) + replace()
    cursor = index + keyword.length
  }
  return result
}
