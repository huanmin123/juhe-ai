import type {
  SecurityPolicyListItem,
  SecurityPolicyMutationResult,
  SecurityPolicyRule,
  SecurityPolicyRuleAction,
  SecurityPolicyRuleType
} from '@/types/domain'
import type { SecurityPolicyPatchPayload } from '@/api/domains/securityPolicies'

/** 规则类型下拉 8 项（契约 §3.2/§3.3，中文标签与后端词表一致）。 */
export const securityPolicyRuleTypeOptions: Array<{ label: string; value: SecurityPolicyRuleType }> = [
  { label: '手机号', value: 'phone' },
  { label: '身份证号', value: 'id_card' },
  { label: '银行卡号', value: 'bank_card' },
  { label: '邮箱', value: 'email' },
  { label: 'IPv4 地址', value: 'ipv4' },
  { label: '密钥形态', value: 'secret' },
  { label: '自定义正则', value: 'custom_regex' },
  { label: '自定义关键词', value: 'custom_keywords' }
]

export const securityPolicyRuleTypeLabels: Record<SecurityPolicyRuleType, string> = {
  phone: '手机号',
  id_card: '身份证号',
  bank_card: '银行卡号',
  email: '邮箱',
  ipv4: 'IPv4 地址',
  secret: '密钥形态',
  custom_regex: '自定义正则',
  custom_keywords: '自定义关键词'
}

/** 动作二选一（一期只有 mask + placeholder，不做 block）。 */
export const securityPolicyRuleActionOptions: Array<{ label: string; value: SecurityPolicyRuleAction }> = [
  { label: '打码（不可逆）', value: 'mask' },
  { label: '占位符（响应自动还原）', value: 'placeholder' }
]

/** placeholder 标签默认词表（契约 §3.3）；label 缺省时按 type 下发默认，前端仅在用户填写时提交。 */
export const securityPolicyDefaultRuleLabel: Record<SecurityPolicyRuleType, string> = {
  phone: 'PHONE',
  id_card: 'ID_CARD',
  bank_card: 'BANK_CARD',
  email: 'EMAIL',
  ipv4: 'IPV4',
  secret: 'SECRET',
  custom_regex: 'CUSTOM',
  custom_keywords: 'CUSTOM'
}

/** 校验约束（契约 §3.3/§3.5）。 */
export const securityPolicyBounds = {
  /** 每策略规则数上限。 */
  ruleCountMax: 50,
  /** custom_regex pattern 长度上限（UTF-8 字节）。 */
  patternMaxBytes: 256,
  /** custom_keywords 关键词个数上限。 */
  keywordsCountMax: 100,
  /** 单个关键词字节上限（UTF-8）。 */
  keywordMaxBytes: 64,
  /** label 形态 [A-Z][A-Z0-9_]{0,30} 的最大长度。 */
  labelMaxLength: 31,
  /** mask 动作可选保留位数范围。 */
  maskKeepMin: 0,
  maskKeepMax: 8
} as const

const securityPolicyLabelPattern = /^[A-Z][A-Z0-9_]{0,30}$/

/** 编辑弹窗单行规则表单模型：custom_keywords 用 tag 输入（string[]），maskKeep 缺省表示跟随 type 默认。 */
export interface SecurityPolicyRuleRowForm {
  id: string
  type: SecurityPolicyRuleType
  action: SecurityPolicyRuleAction
  /** placeholder 动作的可选标签；空串表示按 type 默认词表。 */
  label: string
  /** custom_regex 专属输入。 */
  pattern: string
  /** custom_keywords 专属输入（tag 输入的原始值）。 */
  keywords: string[]
  /** mask 动作可选保留位数；undefined = 按 type 默认。 */
  maskKeep?: number
}

export interface SecurityPolicyFormModel {
  name: string
  description: string
  rules: SecurityPolicyRuleRowForm[]
}

/** 构建提交 payload 形状的完整表单值。 */
export interface SecurityPolicyFormPayload {
  name: string
  description?: string
  rules: SecurityPolicyRule[]
}

let ruleRowSequence = 0

/** 新建空规则行：默认手机号 + 打码（首个类型的可用默认组合）。 */
export function emptySecurityPolicyRuleRow(id?: string): SecurityPolicyRuleRowForm {
  return {
    id: id ?? `rule-${++ruleRowSequence}`,
    type: 'phone',
    action: 'mask',
    label: '',
    pattern: '',
    keywords: [],
    maskKeep: undefined
  }
}

/** 列表行 rules 还原为编辑行模型；非 custom 类型的 pattern/keywords 字段丢弃。 */
export function securityPolicyRuleRowsFromRules(rules: SecurityPolicyRule[]): SecurityPolicyRuleRowForm[] {
  return rules.map((rule) => ({
    id: rule.id,
    type: rule.type,
    action: rule.action,
    label: rule.label ?? '',
    pattern: rule.type === 'custom_regex' ? rule.pattern ?? '' : '',
    keywords: rule.type === 'custom_keywords' ? [...rule.keywords ?? []] : [],
    maskKeep: rule.action === 'mask' ? rule.maskKeep : undefined
  }))
}

export function defaultSecurityPolicyFormModel(): SecurityPolicyFormModel {
  return {
    name: '',
    description: '',
    rules: []
  }
}

/** UTF-8 字节长度（契约的 256/64 字节上限按字节计）。 */
export function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).length
}

/** 单行校验：与后端同词表；返回首个错误文案，通过返回 undefined。 */
export function validateSecurityPolicyRuleRow(row: SecurityPolicyRuleRowForm): string | undefined {
  if (row.type === 'custom_regex') {
    const pattern = row.pattern.trim()
    if (!pattern) return '自定义正则不能为空'
    if (utf8ByteLength(pattern) > securityPolicyBounds.patternMaxBytes) {
      return `自定义正则长度不能超过 ${securityPolicyBounds.patternMaxBytes} 字节`
    }
  }
  if (row.type === 'custom_keywords') {
    const keywords = row.keywords.map((keyword) => keyword.trim()).filter(Boolean)
    if (!keywords.length) return '自定义关键词至少填写 1 个'
    if (keywords.length > securityPolicyBounds.keywordsCountMax) {
      return `自定义关键词不能超过 ${securityPolicyBounds.keywordsCountMax} 个`
    }
    const oversized = keywords.find((keyword) => utf8ByteLength(keyword) > securityPolicyBounds.keywordMaxBytes)
    if (oversized) return `单个关键词不能超过 ${securityPolicyBounds.keywordMaxBytes} 字节：${oversized}`
  }
  const label = row.label.trim()
  if (label && !securityPolicyLabelPattern.test(label)) {
    return '标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31'
  }
  if (row.action === 'mask' && row.maskKeep !== undefined) {
    const keep = Number(row.maskKeep)
    if (!Number.isInteger(keep) || keep < securityPolicyBounds.maskKeepMin || keep > securityPolicyBounds.maskKeepMax) {
      return `保留位数需为 ${securityPolicyBounds.maskKeepMin}-${securityPolicyBounds.maskKeepMax} 的整数`
    }
  }
  return undefined
}

/** 整表校验：规则数上限 + 逐行校验；返回首个错误文案，通过返回 undefined。 */
export function validateSecurityPolicyForm(form: Pick<SecurityPolicyFormModel, 'rules'>): string | undefined {
  if (form.rules.length > securityPolicyBounds.ruleCountMax) {
    return `规则数不能超过 ${securityPolicyBounds.ruleCountMax} 条`
  }
  for (let index = 0; index < form.rules.length; index += 1) {
    const rowError = validateSecurityPolicyRuleRow(form.rules[index]!)
    if (rowError) return `第 ${index + 1} 条规则：${rowError}`
  }
  return undefined
}

/** 行模型 → 提交规则：trim、丢弃空 label、非 custom 类型丢弃专属字段。 */
export function securityPolicyRuleFromRow(row: SecurityPolicyRuleRowForm): SecurityPolicyRule {
  const rule: SecurityPolicyRule = { id: row.id, type: row.type, action: row.action }
  const label = row.label.trim()
  if (label) rule.label = label
  if (row.type === 'custom_regex') rule.pattern = row.pattern.trim()
  if (row.type === 'custom_keywords') {
    rule.keywords = [...new Set(row.keywords.map((keyword) => keyword.trim()).filter(Boolean))]
  }
  if (row.action === 'mask' && row.maskKeep !== undefined) {
    rule.maskKeep = Math.min(securityPolicyBounds.maskKeepMax, Math.max(securityPolicyBounds.maskKeepMin, Math.round(row.maskKeep)))
  }
  return rule
}

export function buildSecurityPolicyFormPayload(form: SecurityPolicyFormModel): SecurityPolicyFormPayload {
  const payload: SecurityPolicyFormPayload = {
    name: form.name.trim(),
    rules: form.rules.map(securityPolicyRuleFromRow)
  }
  const description = form.description.trim()
  if (description) payload.description = description
  return payload
}

const editableFields = [
  'name',
  'description',
  'rules'
] as const

/** 相对打开时 baseline 构建 PATCH delta；description 清空以 null 语义交由 rowPatch 归一。 */
export function buildSecurityPolicyMutationPatch(
  baseline: SecurityPolicyFormPayload,
  current: SecurityPolicyFormPayload
): Omit<SecurityPolicyPatchPayload, 'expectedUpdatedAt'> {
  const patch: Omit<SecurityPolicyPatchPayload, 'expectedUpdatedAt'> = {}
  const writablePatch = patch as Record<string, unknown>
  for (const field of editableFields) {
    if (stableValue(baseline[field]) === stableValue(current[field])) continue
    writablePatch[field] = current[field] ?? null
  }
  return patch
}

export function hasSecurityPolicyMutationChanges(payload: Omit<SecurityPolicyPatchPayload, 'expectedUpdatedAt'>): boolean {
  return editableFields.some((field) => Object.prototype.hasOwnProperty.call(payload, field))
}

export function mergeSecurityPolicyMutationResult(
  current: SecurityPolicyListItem,
  result: SecurityPolicyMutationResult
): SecurityPolicyListItem {
  if (current.id !== result.id) return current
  const patch = result.rowPatch
  const next = { ...current }
  if (patch.name !== undefined) next.name = patch.name
  if (Object.prototype.hasOwnProperty.call(patch, 'description')) {
    if (patch.description === null || patch.description === undefined) delete next.description
    else next.description = patch.description
  }
  if (patch.rules !== undefined) next.rules = patch.rules
  next.updatedAt = result.updatedAt
  return next
}

/** 列表「捕获说明 / 规则摘要」单元格：内置空规则行明示原样转发；有规则时按类型词表去重汇总。 */
export function securityPolicyRulesSummaryText(record: Pick<SecurityPolicyListItem, 'builtIn' | 'rules'>): string {
  if (!record.rules.length) return '空规则（请求原样转发）'
  const typeLabels: string[] = []
  for (const rule of record.rules) {
    const label = securityPolicyRuleTypeLabels[rule.type]
    if (!typeLabels.includes(label)) typeLabels.push(label)
  }
  const visible = typeLabels.slice(0, 4).join('、')
  return `${record.rules.length} 条规则：${visible}${typeLabels.length > 4 ? ' 等' : ''}`
}

function stableValue(value: unknown): string {
  if (value === undefined) return 'undefined'
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? String(value)
  if (Array.isArray(value)) return `[${value.map(stableValue).join(',')}]`
  const record = value as Record<string, unknown>
  return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableValue(record[key])}`).join(',')}}`
}
