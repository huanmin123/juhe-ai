import { describe, expect, it } from 'vitest'

import type { SecurityPolicyListItem, SecurityPolicyMutationResult, SecurityPolicyRule } from '@/types/domain'
import {
  buildSecurityPolicyFormPayload,
  buildSecurityPolicyMutationPatch,
  defaultSecurityPolicyFormModel,
  emptySecurityPolicyRuleRow,
  hasSecurityPolicyMutationChanges,
  mergeSecurityPolicyMutationResult,
  securityPolicyDefaultRuleLabel,
  securityPolicyRuleFromRow,
  securityPolicyRuleRowsFromRules,
  securityPolicyRuleTypeLabels,
  securityPolicyRuleTypeOptions,
  securityPolicyRulesSummaryText,
  utf8ByteLength,
  validateSecurityPolicyForm,
  validateSecurityPolicyRuleRow,
  type SecurityPolicyFormPayload
} from './securityPoliciesForm'
import { trialScanSecurityPolicyText } from './securityPoliciesTrialScan'

describe('label 词表与类型选项', () => {
  it('类型下拉 8 项且中文标签与词表一致', () => {
    expect(securityPolicyRuleTypeOptions).toHaveLength(8)
    expect(securityPolicyRuleTypeOptions.map((option) => option.label)).toEqual([
      '手机号',
      '身份证号',
      '银行卡号',
      '邮箱',
      'IPv4 地址',
      '密钥形态',
      '自定义正则',
      '自定义关键词'
    ])
    for (const option of securityPolicyRuleTypeOptions) {
      expect(securityPolicyRuleTypeLabels[option.value]).toBe(option.label)
    }
  })

  it('placeholder 默认标签按契约词表', () => {
    expect(securityPolicyDefaultRuleLabel).toEqual({
      phone: 'PHONE',
      id_card: 'ID_CARD',
      bank_card: 'BANK_CARD',
      email: 'EMAIL',
      ipv4: 'IPV4',
      secret: 'SECRET',
      custom_regex: 'CUSTOM',
      custom_keywords: 'CUSTOM'
    })
  })
})

describe('表单模型与规则增删', () => {
  it('默认表单为空规则集', () => {
    expect(defaultSecurityPolicyFormModel()).toEqual({ name: '', description: '', rules: [] })
  })

  it('新增行默认手机号 + 打码，行 ID 唯一', () => {
    const first = emptySecurityPolicyRuleRow()
    const second = emptySecurityPolicyRuleRow()
    expect(first.type).toBe('phone')
    expect(first.action).toBe('mask')
    expect(first.maskKeep).toBeUndefined()
    expect(second.id).not.toBe(first.id)
  })

  it('显式注入行 ID（编辑回显）与自增 ID 不冲突', () => {
    const row = emptySecurityPolicyRuleRow('r1')
    expect(row.id).toBe('r1')
  })

  it('securityPolicyRuleRowsFromRules 还原编辑行并丢弃非 custom 类型专属字段', () => {
    const rules: SecurityPolicyRule[] = [
      { id: 'r1', type: 'phone', action: 'mask', maskKeep: 3, label: 'PHONE' },
      { id: 'r2', type: 'custom_regex', action: 'placeholder', pattern: 'GH-[0-9]{6}' },
      { id: 'r3', type: 'custom_keywords', action: 'mask', keywords: ['XX项目', 'XX-2026'] },
      { id: 'r4', type: 'email', action: 'placeholder' }
    ]
    const rows = securityPolicyRuleRowsFromRules(rules)
    expect(rows[0]).toMatchObject({ id: 'r1', type: 'phone', action: 'mask', label: 'PHONE', maskKeep: 3, pattern: '', keywords: [] })
    expect(rows[1]).toMatchObject({ id: 'r2', pattern: 'GH-[0-9]{6}', keywords: [] })
    expect(rows[2]).toMatchObject({ id: 'r3', keywords: ['XX项目', 'XX-2026'] })
    // placeholder 动作不保留 maskKeep
    expect(rows[3]?.maskKeep).toBeUndefined()
  })

  it('securityPolicyRuleFromRow 规范化：去空 label、关键词去重去空、maskKeep 收敛 0..8', () => {
    const rule = securityPolicyRuleFromRow({
      id: 'r1',
      type: 'custom_keywords',
      action: 'mask',
      label: ' PROJECT_CODE ',
      pattern: '',
      keywords: [' XX项目 ', 'XX项目', '', 'XX-2026'],
      maskKeep: 12
    })
    expect(rule).toEqual({
      id: 'r1',
      type: 'custom_keywords',
      action: 'mask',
      label: 'PROJECT_CODE',
      keywords: ['XX项目', 'XX-2026'],
      maskKeep: 8
    })
  })

  it('内置类型不携带 pattern/keywords，非 mask 不携带 maskKeep', () => {
    expect(securityPolicyRuleFromRow({ id: 'r1', type: 'phone', action: 'placeholder', label: '', pattern: '(', keywords: ['x'], maskKeep: 2 }))
      .toEqual({ id: 'r1', type: 'phone', action: 'placeholder' })
  })
})

describe('校验边界', () => {
  it('custom_regex 必填且受 256 字节限制（按 UTF-8 字节计）', () => {
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'mask', label: '', pattern: '   ', keywords: [], maskKeep: undefined }))
      .toBe('自定义正则不能为空')
    const longPattern = 'a'.repeat(257)
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'mask', label: '', pattern: longPattern, keywords: [], maskKeep: undefined }))
      .toBe('自定义正则长度不能超过 256 字节')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'mask', label: '', pattern: 'a'.repeat(200) + '中'.repeat(28), keywords: [], maskKeep: undefined }))
      .toBe('自定义正则长度不能超过 256 字节')
    expect(utf8ByteLength('中文')).toBe(6)
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'mask', label: '', pattern: 'GH-[0-9]{6}', keywords: [], maskKeep: undefined }))
      .toBeUndefined()
  })

  it('custom_keywords 至少 1 个、≤100 个、单个 ≤64 字节', () => {
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_keywords', action: 'mask', label: '', pattern: '', keywords: [' ', ''], maskKeep: undefined }))
      .toBe('自定义关键词至少填写 1 个')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_keywords', action: 'mask', label: '', pattern: '', keywords: Array.from({ length: 101 }, (_, i) => `k${i}`), maskKeep: undefined }))
      .toBe('自定义关键词不能超过 100 个')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_keywords', action: 'mask', label: '', pattern: '', keywords: ['中'.repeat(32)], maskKeep: undefined }))
      .toBe('单个关键词不能超过 64 字节：' + '中'.repeat(32))
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_keywords', action: 'mask', label: '', pattern: '', keywords: ['XX项目'], maskKeep: undefined }))
      .toBeUndefined()
  })

  it('label 词表 [A-Z][A-Z0-9_]{0,30}：可空、合法通过、非法拒绝', () => {
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'placeholder', label: '', pattern: '', keywords: [], maskKeep: undefined })).toBeUndefined()
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'placeholder', label: 'PHONE_2', pattern: '', keywords: [], maskKeep: undefined })).toBeUndefined()
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'placeholder', label: '2PHONE', pattern: '', keywords: [], maskKeep: undefined }))
      .toBe('标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'placeholder', label: 'phone', pattern: '', keywords: [], maskKeep: undefined }))
      .toBe('标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'placeholder', label: 'A'.repeat(32), pattern: '', keywords: [], maskKeep: undefined }))
      .toBe('标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'placeholder', label: 'A'.repeat(31), pattern: '', keywords: [], maskKeep: undefined })).toBeUndefined()
  })

  it('maskKeep 可选且限定 0..8 整数', () => {
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'mask', label: '', pattern: '', keywords: [], maskKeep: undefined })).toBeUndefined()
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'mask', label: '', pattern: '', keywords: [], maskKeep: 8 })).toBeUndefined()
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'mask', label: '', pattern: '', keywords: [], maskKeep: 9 }))
      .toBe('保留位数需为 0-8 的整数')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'mask', label: '', pattern: '', keywords: [], maskKeep: -1 }))
      .toBe('保留位数需为 0-8 的整数')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'phone', action: 'mask', label: '', pattern: '', keywords: [], maskKeep: 1.5 }))
      .toBe('保留位数需为 0-8 的整数')
  })

  it('整表校验带行序号与规则数上限', () => {
    const form = defaultSecurityPolicyFormModel()
    expect(validateSecurityPolicyForm(form)).toBeUndefined()
    form.rules.push({ id: 'r1', type: 'phone', action: 'mask', label: 'BAD LABEL', pattern: '', keywords: [], maskKeep: undefined })
    expect(validateSecurityPolicyForm(form)).toBe('第 1 条规则：标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyForm({ rules: Array.from({ length: 51 }, () => emptySecurityPolicyRuleRow()) }))
      .toBe('规则数不能超过 50 条')
  })
})

describe('buildSecurityPolicyFormPayload 与 PATCH delta', () => {
  it('payload trim 名称与说明，空说明不携带', () => {
    const form = defaultSecurityPolicyFormModel()
    form.name = ' 策略 A '
    form.description = '  '
    form.rules.push(emptySecurityPolicyRuleRow('r1'))
    expect(buildSecurityPolicyFormPayload(form)).toEqual({
      name: '策略 A',
      rules: [{ id: 'r1', type: 'phone', action: 'mask' }]
    })
    form.description = '出口脱敏'
    expect(buildSecurityPolicyFormPayload(form).description).toBe('出口脱敏')
  })

  it('未修改时生成空 delta；改说明发送 description；清空说明发送 null', () => {
    const baseline: SecurityPolicyFormPayload = { name: 'A', description: '说明', rules: [{ id: 'r1', type: 'phone', action: 'mask' }] }
    expect(buildSecurityPolicyMutationPatch(baseline, { ...baseline, rules: [...baseline.rules] })).toEqual({})
    expect(hasSecurityPolicyMutationChanges(buildSecurityPolicyMutationPatch(baseline, { ...baseline, rules: [...baseline.rules] }))).toBe(false)
    expect(buildSecurityPolicyMutationPatch(baseline, { ...baseline, description: '新说明' })).toEqual({ description: '新说明' })
    expect(buildSecurityPolicyMutationPatch(baseline, { name: 'A', rules: [...baseline.rules] })).toEqual({ description: null })
    expect(buildSecurityPolicyMutationPatch(baseline, { name: 'B', description: '说明', rules: [...baseline.rules] })).toEqual({ name: 'B' })
  })

  it('rules 变化发送整体数组', () => {
    const baseline = { name: 'A', rules: [{ id: 'r1', type: 'phone', action: 'mask' }] as SecurityPolicyRule[] }
    const current = { name: 'A', rules: [...baseline.rules, { id: 'r2', type: 'email', action: 'placeholder' } as SecurityPolicyRule] }
    const patch = buildSecurityPolicyMutationPatch(baseline, current)
    expect(patch.rules).toEqual(current.rules)
  })
})

describe('mergeSecurityPolicyMutationResult', () => {
  const row: SecurityPolicyListItem = {
    id: 'sp-1',
    name: '旧名',
    description: '旧说明',
    rules: [{ id: 'r1', type: 'phone', action: 'mask' }],
    builtIn: false,
    boundApiKeyCount: 2,
    protectionCount: 0,
    updatedAt: '2026-01-01T00:00:00Z'
  }

  it('应用 rowPatch 并推进 updatedAt', () => {
    const result: SecurityPolicyMutationResult = {
      id: 'sp-1',
      changedFields: ['name', 'rules'],
      rowPatch: { name: '新名', rules: [{ id: 'r1', type: 'phone', action: 'placeholder', label: 'PHONE' }] },
      updatedAt: '2026-02-02T00:00:00Z'
    }
    const merged = mergeSecurityPolicyMutationResult(row, result)
    expect(merged.name).toBe('新名')
    expect(merged.rules).toEqual([{ id: 'r1', type: 'phone', action: 'placeholder', label: 'PHONE' }])
    expect(merged.updatedAt).toBe('2026-02-02T00:00:00Z')
    expect(merged.description).toBe('旧说明')
  })

  it('description null 归一为缺失', () => {
    const merged = mergeSecurityPolicyMutationResult(row, {
      id: 'sp-1',
      changedFields: ['description'],
      rowPatch: { description: null },
      updatedAt: '2026-03-03T00:00:00Z'
    })
    expect(merged.description).toBeUndefined()
  })

  it('id 不同时不合并', () => {
    expect(mergeSecurityPolicyMutationResult(row, { id: 'other', changedFields: [], rowPatch: { name: '别的行' }, updatedAt: '2026-04-04T00:00:00Z' }).name).toBe('旧名')
  })
})

describe('securityPolicyRulesSummaryText', () => {
  it('空规则显示原样转发说明', () => {
    expect(securityPolicyRulesSummaryText({ builtIn: true, rules: [] })).toBe('空规则（请求原样转发）')
    expect(securityPolicyRulesSummaryText({ builtIn: false, rules: [] })).toBe('空规则（请求原样转发）')
  })

  it('按类型词表去重汇总并标注条数', () => {
    const rules: SecurityPolicyRule[] = [
      { id: 'r1', type: 'phone', action: 'mask' },
      { id: 'r2', type: 'email', action: 'placeholder' },
      { id: 'r3', type: 'phone', action: 'placeholder' },
      { id: 'r4', type: 'custom_regex', action: 'mask' }
    ]
    expect(securityPolicyRulesSummaryText({ builtIn: false, rules })).toBe('4 条规则：手机号、邮箱、自定义正则')
  })

  it('类型超过 4 种时截断加等', () => {
    const rules: SecurityPolicyRule[] = [
      { id: 'r1', type: 'phone', action: 'mask' },
      { id: 'r2', type: 'email', action: 'mask' },
      { id: 'r3', type: 'ipv4', action: 'mask' },
      { id: 'r4', type: 'secret', action: 'mask' },
      { id: 'r5', type: 'bank_card', action: 'mask' }
    ]
    expect(securityPolicyRulesSummaryText({ builtIn: false, rules })).toBe('5 条规则：手机号、邮箱、IPv4 地址、密钥形态 等')
  })
})

describe('试扫本地演示（简化，以服务端规则为准）', () => {
  it('mask 手机号按保留位数打码（演示为对称保留近似）', () => {
    const result = trialScanSecurityPolicyText([{ id: 'r1', type: 'phone', action: 'mask', maskKeep: 3 }], '联系 13800138000')
    expect(result.output).toBe('联系 138*****000')
    expect(result.hits[0]).toMatchObject({ ruleLabel: 'PHONE', count: 1 })
    expect(result.hits[0]?.samples[0]).toBe('13800138000 → 138*****000')
  })

  it('placeholder 按标签顺序编号且同值复用编号', () => {
    const result = trialScanSecurityPolicyText(
      [{ id: 'r1', type: 'email', action: 'placeholder', label: 'EMAIL' }],
      'a@b.com 与 a@b.com 以及 c@d.cn'
    )
    expect(result.output).toBe('[[EMAIL_1]] 与 [[EMAIL_1]] 以及 [[EMAIL_2]]')
    expect(result.hits[0]?.count).toBe(3)
  })

  it('custom_keywords 字面命中计数（大小写敏感、多次出现累计）', () => {
    const result = trialScanSecurityPolicyText(
      [{ id: 'r1', type: 'custom_keywords', action: 'mask', keywords: ['XX项目'] }],
      'XX项目启动，xx项目不命中，XX项目收尾'
    )
    expect(result.hits[0]?.count).toBe(2)
  })

  it('custom_regex 编译失败时标记不支持且不改写文本', () => {
    const result = trialScanSecurityPolicyText([{ id: 'r1', type: 'custom_regex', action: 'mask', pattern: '(' }], '原文')
    expect(result.output).toBe('原文')
    expect(result.hits[0]?.count).toBe(0)
    expect(result.hits[0]?.unsupportedReason).toBeTruthy()
  })

  it('ipv4 演示保留首段', () => {
    const result = trialScanSecurityPolicyText([{ id: 'r1', type: 'ipv4', action: 'mask' }], '内网 10.2.3.4 与版本号 1.2.33.44')
    expect(result.output).toBe('内网 10.*.*.* 与版本号 1.*.*.*')
  })
})
