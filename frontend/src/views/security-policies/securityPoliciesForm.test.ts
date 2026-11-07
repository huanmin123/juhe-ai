import { describe, expect, it } from 'vitest'

import type { SecurityPolicyListItem, SecurityPolicyMutationResult, SecurityPolicyRule } from '@/types/domain'
import {
  applySecurityPolicyMatrixAction,
  buildSecurityPolicyFormPayload,
  buildSecurityPolicyMutationPatch,
  builtinSecurityPolicyRuleRow,
  defaultSecurityPolicyFormModel,
  emptySecurityPolicyCustomRuleRow,
  hasSecurityPolicyMutationChanges,
  isSecurityPolicyMatrixSlotRow,
  mergeSecurityPolicyMutationResult,
  securityPolicyBuiltInMatrixLabels,
  securityPolicyBuiltInMatrixTypes,
  securityPolicyCustomRuleTypeOptions,
  securityPolicyDefaultRuleLabel,
  securityPolicyMatrixActionOptions,
  securityPolicyMatrixSlots,
  securityPolicyRuleFromRow,
  securityPolicyRuleRowsFromRules,
  securityPolicyRuleTypeLabels,
  securityPolicyRuleTypeOptions,
  securityPolicyRulesSummaryText,
  utf8ByteLength,
  validateSecurityPolicyForm,
  validateSecurityPolicyRuleRow,
  type SecurityPolicyFormPayload,
  type SecurityPolicyRuleRowForm
} from './securityPoliciesForm'
import { trialScanSecurityPolicyText } from './securityPoliciesTrialScan'

/** 构造自定义规则行测试样本（可覆盖字段）。 */
function customRow(overrides: Partial<SecurityPolicyRuleRowForm> = {}): SecurityPolicyRuleRowForm {
  return { ...emptySecurityPolicyCustomRuleRow('r'), ...overrides }
}

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

  it('内置敏感信息矩阵固定行序与展示名', () => {
    expect([...securityPolicyBuiltInMatrixTypes]).toEqual(['phone', 'email', 'id_card', 'bank_card', 'ipv4', 'secret'])
    expect(securityPolicyBuiltInMatrixLabels).toEqual({
      phone: '手机号',
      email: '邮箱',
      id_card: '身份证号',
      bank_card: '银行卡号',
      ipv4: 'IPv4 地址',
      secret: '密钥形态'
    })
  })

  it('矩阵动作三选一：不处理 / 打码 / 占位符', () => {
    expect(securityPolicyMatrixActionOptions.map((option) => [option.value, option.label])).toEqual([
      ['off', '不处理'],
      ['mask', '打码（不可逆）'],
      ['placeholder', '占位符（响应自动还原）']
    ])
  })
})

describe('表单模型与规则增删', () => {
  it('默认表单为空规则集', () => {
    expect(defaultSecurityPolicyFormModel()).toEqual({ name: '', description: '', rules: [] })
  })

  it('新建时矩阵 6 行默认渲染且全部「不处理」', () => {
    const slots = securityPolicyMatrixSlots(defaultSecurityPolicyFormModel().rules)
    expect(slots.map((slot) => slot.type)).toEqual([...securityPolicyBuiltInMatrixTypes])
    expect(slots.map((slot) => slot.name)).toEqual(['手机号', '邮箱', '身份证号', '银行卡号', 'IPv4 地址', '密钥形态'])
    for (const slot of slots) {
      expect(slot.action).toBe('off')
      expect(slot.row).toBeUndefined()
    }
  })

  it('新增自定义行默认自定义正则 + 打码，行 ID 唯一', () => {
    const first = emptySecurityPolicyCustomRuleRow()
    const second = emptySecurityPolicyCustomRuleRow()
    expect(first.type).toBe('custom_regex')
    expect(first.action).toBe('mask')
    expect(first.maskKeep).toBeUndefined()
    expect(second.id).not.toBe(first.id)
  })

  it('显式注入行 ID（编辑回显）与自增 ID 不冲突', () => {
    const row = emptySecurityPolicyCustomRuleRow('r1')
    expect(row.id).toBe('r1')
  })

  it('内置矩阵行工厂：占位符预填默认 label，打码 label 留空', () => {
    expect(builtinSecurityPolicyRuleRow('bank_card', 'placeholder')).toMatchObject({ type: 'bank_card', action: 'placeholder', label: 'BANK_CARD' })
    expect(builtinSecurityPolicyRuleRow('email', 'mask')).toMatchObject({ type: 'email', action: 'mask', label: '', maskKeep: undefined })
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

  it('编辑回显分流：内置 type 进矩阵槽位、custom 进自定义区', () => {
    const rules: SecurityPolicyRule[] = [
      { id: 'r1', type: 'phone', action: 'placeholder', label: 'PHONE' },
      { id: 'r2', type: 'custom_regex', action: 'placeholder', pattern: 'GH-[0-9]{6}' },
      { id: 'r3', type: 'email', action: 'mask', maskKeep: 2 }
    ]
    const rows = securityPolicyRuleRowsFromRules(rules)
    const slots = securityPolicyMatrixSlots(rows)
    expect(slots.find((slot) => slot.type === 'phone')?.row?.id).toBe('r1')
    expect(slots.find((slot) => slot.type === 'email')?.row?.id).toBe('r3')
    expect(slots.find((slot) => slot.type === 'email')?.action).toBe('mask')
    expect(slots.find((slot) => slot.type === 'id_card')?.action).toBe('off')
    expect(rows.filter((row) => !isSecurityPolicyMatrixSlotRow(row)).map((row) => row.id)).toEqual(['r2'])
  })

  it('编辑回显：内置占位符 label 缺省按词表补默认（矩阵占位标签必填）', () => {
    const rows = securityPolicyRuleRowsFromRules([{ id: 'r1', type: 'bank_card', action: 'placeholder' }])
    expect(rows[0]?.label).toBe('BANK_CARD')
  })

  it('重复内置 type 保真兜底：第一条进矩阵槽位，其余标记 overflow 留自定义区且 payload 原样保留', () => {
    const rules: SecurityPolicyRule[] = [
      { id: 'r1', type: 'phone', action: 'mask' },
      { id: 'r2', type: 'phone', action: 'placeholder', label: 'PHONE_2' }
    ]
    const rows = securityPolicyRuleRowsFromRules(rules)
    expect(isSecurityPolicyMatrixSlotRow(rows[0]!)).toBe(true)
    expect(rows[1]?.overflowBuiltIn).toBe(true)
    expect(isSecurityPolicyMatrixSlotRow(rows[1]!)).toBe(false)
    const payload = buildSecurityPolicyFormPayload({ ...defaultSecurityPolicyFormModel(), rules: rows })
    expect(payload.rules).toEqual(rules)
    // overflow 行不占槽位：槽位仍指向第一条
    expect(securityPolicyMatrixSlots(rows).find((slot) => slot.type === 'phone')?.row?.id).toBe('r1')
  })

  it('未知/脏 type 兜底进自定义区不丢数据，类型选项原样展示自身 type', () => {
    const rows = securityPolicyRuleRowsFromRules([{ id: 'r1', type: 'mystery' as SecurityPolicyRule['type'], action: 'mask' }])
    expect(isSecurityPolicyMatrixSlotRow(rows[0]!)).toBe(false)
    expect(securityPolicyCustomRuleTypeOptions(rows[0]!)[0]).toEqual({ label: 'mystery', value: 'mystery' })
    const payload = buildSecurityPolicyFormPayload({ ...defaultSecurityPolicyFormModel(), rules: rows })
    expect(payload.rules).toEqual([{ id: 'r1', type: 'mystery', action: 'mask' }])
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

describe('内置矩阵行动作变化（rules upsert/remove）', () => {
  it('切到占位符生成带默认 label 的规则', () => {
    const rules = applySecurityPolicyMatrixAction([], 'phone', 'placeholder')
    expect(rules).toHaveLength(1)
    expect(rules[0]).toMatchObject({ type: 'phone', action: 'placeholder', label: 'PHONE' })
  })

  it('切到打码生成 mask 规则，用户填保留位数后随 payload 携带 maskKeep', () => {
    const rules = applySecurityPolicyMatrixAction([], 'email', 'mask')
    expect(rules[0]).toMatchObject({ type: 'email', action: 'mask', label: '', maskKeep: undefined })
    rules[0]!.maskKeep = 3
    expect(securityPolicyRuleFromRow(rules[0]!)).toEqual({ id: rules[0]!.id, type: 'email', action: 'mask', maskKeep: 3 })
  })

  it('打码 ↔ 占位符切换保留 label（打码不使用、切回占位符复用），占位符不携带 maskKeep', () => {
    let rules = applySecurityPolicyMatrixAction([], 'phone', 'placeholder')
    rules = applySecurityPolicyMatrixAction(rules, 'phone', 'mask')
    expect(rules[0]).toMatchObject({ action: 'mask', label: 'PHONE' })
    rules[0]!.maskKeep = 2
    rules = applySecurityPolicyMatrixAction(rules, 'phone', 'placeholder')
    expect(rules[0]).toMatchObject({ action: 'placeholder', label: 'PHONE' })
    expect(securityPolicyRuleFromRow(rules[0]!)).toEqual({ id: rules[0]!.id, type: 'phone', action: 'placeholder', label: 'PHONE' })
  })

  it('占位符 label 被清空后切打码再切回，按词表补默认 label', () => {
    let rules = applySecurityPolicyMatrixAction([], 'phone', 'placeholder')
    rules[0]!.label = '   '
    rules = applySecurityPolicyMatrixAction(rules, 'phone', 'mask')
    rules = applySecurityPolicyMatrixAction(rules, 'phone', 'placeholder')
    expect(rules[0]?.label).toBe('PHONE')
  })

  it('切回不处理移除该 type 规则；无行时 off 为幂等 no-op', () => {
    let rules = applySecurityPolicyMatrixAction([], 'secret', 'mask')
    rules = applySecurityPolicyMatrixAction(rules, 'secret', 'off')
    expect(rules).toEqual([])
    expect(securityPolicyMatrixSlots(rules).every((slot) => slot.action === 'off')).toBe(true)
    const empty: SecurityPolicyRuleRowForm[] = []
    expect(applySecurityPolicyMatrixAction(empty, 'secret', 'off')).toBe(empty)
  })

  it('矩阵 upsert 不影响自定义行与 overflow 保真行', () => {
    const base = securityPolicyRuleRowsFromRules([
      { id: 'r1', type: 'phone', action: 'mask' },
      { id: 'r2', type: 'phone', action: 'mask', maskKeep: 1 },
      { id: 'r3', type: 'custom_regex', action: 'mask', pattern: 'X' }
    ])
    const rules = applySecurityPolicyMatrixAction(base, 'phone', 'placeholder')
    expect(rules.find((row) => row.id === 'r1')).toMatchObject({ action: 'placeholder', label: 'PHONE' })
    expect(rules.find((row) => row.id === 'r2')).toMatchObject({ action: 'mask', overflowBuiltIn: true })
    expect(rules.find((row) => row.id === 'r3')?.pattern).toBe('X')
  })
})

describe('自定义规则行', () => {
  it('类型选项仅自定义两类；overflow/未知 type 的兜底行原样展示自身 type', () => {
    expect(securityPolicyCustomRuleTypeOptions({ type: 'custom_keywords' }).map((option) => option.value))
      .toEqual(['custom_regex', 'custom_keywords'])
    const fallback = securityPolicyCustomRuleTypeOptions({ type: 'phone' })
    expect(fallback[0]).toEqual({ label: '手机号', value: 'phone' })
    expect(fallback.map((option) => option.value)).toEqual(['phone', 'custom_regex', 'custom_keywords'])
  })

  it('自定义行与内置矩阵规则共存，payload 顺序保留', () => {
    const form = {
      ...defaultSecurityPolicyFormModel(),
      rules: securityPolicyRuleRowsFromRules([
        { id: 'r1', type: 'secret', action: 'mask' },
        { id: 'r2', type: 'custom_keywords', action: 'placeholder', keywords: ['XX项目'] }
      ])
    }
    expect(isSecurityPolicyMatrixSlotRow(form.rules[0]!)).toBe(true)
    expect(isSecurityPolicyMatrixSlotRow(form.rules[1]!)).toBe(false)
    expect(buildSecurityPolicyFormPayload(form).rules).toEqual([
      { id: 'r1', type: 'secret', action: 'mask' },
      { id: 'r2', type: 'custom_keywords', action: 'placeholder', keywords: ['XX项目'] }
    ])
  })

  it('自定义行总数受 ≤50 限制（含内置生效规则一起计数）', () => {
    const form = defaultSecurityPolicyFormModel()
    form.rules = [
      ...securityPolicyRuleRowsFromRules([{ id: 'r1', type: 'phone', action: 'mask' }]),
      ...Array.from({ length: 49 }, (_, index) => customRow({ id: `k${index}`, pattern: 'a' }))
    ]
    expect(validateSecurityPolicyForm(form)).toBeUndefined()
    form.rules.push(customRow({ id: 'k49', pattern: 'a' }))
    expect(validateSecurityPolicyForm(form)).toBe('规则数不能超过 50 条')
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

  it('内置行占位符 label 必填；overflow 保真行维持 legacy 可空语义', () => {
    expect(validateSecurityPolicyRuleRow(customRow({ type: 'phone', action: 'placeholder', label: '' })))
      .toBe('内置敏感信息的占位标签不能为空')
    expect(validateSecurityPolicyRuleRow(customRow({ type: 'phone', action: 'placeholder', label: '  ' })))
      .toBe('内置敏感信息的占位标签不能为空')
    expect(validateSecurityPolicyRuleRow(customRow({ type: 'phone', action: 'placeholder', label: '', overflowBuiltIn: true })))
      .toBeUndefined()
    expect(validateSecurityPolicyRuleRow(customRow({ type: 'phone', action: 'placeholder', label: 'PHONE' })))
      .toBeUndefined()
    expect(validateSecurityPolicyRuleRow(customRow({ type: 'phone', action: 'mask', label: '' })))
      .toBeUndefined()
  })

  it('label 词表 [A-Z][A-Z0-9_]{0,30}：自定义行可空、合法通过、非法拒绝', () => {
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'placeholder', label: '', pattern: 'a', keywords: [], maskKeep: undefined })).toBeUndefined()
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'placeholder', label: 'PHONE_2', pattern: 'a', keywords: [], maskKeep: undefined })).toBeUndefined()
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'placeholder', label: '2PHONE', pattern: 'a', keywords: [], maskKeep: undefined }))
      .toBe('标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'placeholder', label: 'phone', pattern: 'a', keywords: [], maskKeep: undefined }))
      .toBe('标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'placeholder', label: 'A'.repeat(32), pattern: 'a', keywords: [], maskKeep: undefined }))
      .toBe('标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyRuleRow({ id: 'r', type: 'custom_regex', action: 'placeholder', label: 'A'.repeat(31), pattern: 'a', keywords: [], maskKeep: undefined })).toBeUndefined()
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
    form.rules.push({ id: 'r1', type: 'custom_regex', action: 'mask', label: 'BAD LABEL', pattern: 'a', keywords: [], maskKeep: undefined })
    expect(validateSecurityPolicyForm(form)).toBe('第 1 条规则：标签需以大写字母开头，仅含大写字母、数字与下划线，长度 ≤31')
    expect(validateSecurityPolicyForm({ rules: Array.from({ length: 51 }, () => emptySecurityPolicyCustomRuleRow()) }))
      .toBe('规则数不能超过 50 条')
  })
})

describe('buildSecurityPolicyFormPayload 与 PATCH delta', () => {
  it('payload trim 名称与说明，空说明不携带', () => {
    const form = defaultSecurityPolicyFormModel()
    form.name = ' 策略 A '
    form.description = '  '
    form.rules.push(builtinSecurityPolicyRuleRow('phone', 'mask', 'r1'))
    expect(buildSecurityPolicyFormPayload(form)).toEqual({
      name: '策略 A',
      rules: [{ id: 'r1', type: 'phone', action: 'mask' }]
    })
    form.description = '出口脱敏'
    expect(buildSecurityPolicyFormPayload(form).description).toBe('出口脱敏')
  })

  it('编辑回显 → 重建 payload 与 baseline 相同（无脏 diff，delta 为空）', () => {
    const baselineRules: SecurityPolicyRule[] = [
      { id: 'r1', type: 'phone', action: 'placeholder', label: 'PHONE' },
      { id: 'r2', type: 'email', action: 'mask', maskKeep: 2 },
      { id: 'r3', type: 'custom_regex', action: 'mask', pattern: 'GH-[0-9]{6}' }
    ]
    const baseline: SecurityPolicyFormPayload = { name: 'A', rules: baselineRules }
    const rebuilt = buildSecurityPolicyFormPayload({
      ...defaultSecurityPolicyFormModel(),
      name: 'A',
      rules: securityPolicyRuleRowsFromRules(baselineRules)
    })
    expect(rebuilt.rules).toEqual(baselineRules)
    expect(buildSecurityPolicyMutationPatch(baseline, rebuilt)).toEqual({})
    expect(hasSecurityPolicyMutationChanges(buildSecurityPolicyMutationPatch(baseline, rebuilt))).toBe(false)
  })

  it('未修改时生成空 delta；改说明发送 description；清空说明发送 null', () => {
    const baseline: SecurityPolicyFormPayload = { name: 'A', description: '说明', rules: [{ id: 'r1', type: 'phone', action: 'mask' }] }
    expect(buildSecurityPolicyMutationPatch(baseline, { ...baseline, rules: [...baseline.rules] })).toEqual({})
    expect(hasSecurityPolicyMutationChanges(buildSecurityPolicyMutationPatch(baseline, { ...baseline, rules: [...baseline.rules] }))).toBe(false)
    expect(buildSecurityPolicyMutationPatch(baseline, { ...baseline, description: '新说明' })).toEqual({ description: '新说明' })
    expect(buildSecurityPolicyMutationPatch(baseline, { name: 'A', rules: [...baseline.rules] })).toEqual({ description: null })
    expect(buildSecurityPolicyMutationPatch(baseline, { name: 'B', description: '说明', rules: [...baseline.rules] })).toEqual({ name: 'B' })
  })

  it('仅 rules 变化时 delta 发送整体数组（矩阵切回不处理 → rules: []）', () => {
    const baseline = buildSecurityPolicyFormPayload({
      ...defaultSecurityPolicyFormModel(),
      name: 'A',
      rules: applySecurityPolicyMatrixAction([], 'phone', 'placeholder')
    })
    expect(baseline.rules).toEqual([{ id: expect.any(String), type: 'phone', action: 'placeholder', label: 'PHONE' }])
    const current: SecurityPolicyFormPayload = { name: 'A', rules: [] }
    const patch = buildSecurityPolicyMutationPatch(baseline, current)
    expect(patch).toEqual({ rules: [] })
    expect(hasSecurityPolicyMutationChanges(patch)).toBe(true)
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
