import { describe, expect, it } from 'vitest'

import { defaultSystemSettings } from './settingsForm'
import {
  groupCustomCount,
  groupFieldKeys,
  isCustomValue,
  settingGroups,
  settingsSectionFields,
  settingsTotalItemCount,
  type SettingsDisplayAccess
} from './settingsDisplay'

/** 全部取默认值的 access（等价于刚装好的库）。 */
function defaultAccess(): SettingsDisplayAccess {
  const defaults: Record<string, string | number> = {
    appName: '聚合 AI',
    appIcon: '/__aisys__/brand-icon.svg'
  }
  for (const [key, value] of Object.entries(defaultSystemSettings)) {
    if (typeof value !== 'object') defaults[key] = value
  }
  for (const subKey of ['codex', 'claudeCode', 'geminiCLI', 'zcode', 'grokCLI'] as const) {
    defaults[`upstreamClientVersionOverrides.${subKey}`] = ''
  }
  return {
    baseline: (key) => defaults[key],
    isCustom: (key) => isCustomValue(key, defaults[key])
  }
}

describe('组覆盖完整性（防字段漏挂）', () => {
  it('7 组共 46 个可编辑字段', () => {
    expect(settingGroups).toHaveLength(7)
    expect(settingsTotalItemCount).toBe(46)
    expect(settingGroups.reduce((sum, g) => sum + groupFieldKeys(g).length, 0)).toBe(46)
  })

  it('组字段的 section 归属并集 === section 契约字段（overrides 按子键展开）', () => {
    const covered = new Set<string>()
    for (const group of settingGroups) {
      for (const key of groupFieldKeys(group)) covered.add(key)
    }
    const contract = new Set<string>()
    for (const [sectionKey, keys] of Object.entries(settingsSectionFields)) {
      if (sectionKey === 'brand') {
        keys.forEach((k) => contract.add(k))
      } else if (sectionKey === 'gateway-core') {
        keys.filter((k) => k !== 'upstreamClientVersionOverrides').forEach((k) => contract.add(k))
        ;['codex', 'claudeCode', 'geminiCLI', 'zcode', 'grokCLI'].forEach((k) => contract.add(`upstreamClientVersionOverrides.${k}`))
      } else {
        keys.forEach((k) => contract.add(k))
      }
    }
    expect([...covered].sort()).toEqual([...contract].sort())
  })

  it('每个组的 sectionKeys 与其字段的实际归属一致', () => {
    for (const group of settingGroups) {
      const actual = new Set(groupFieldKeys(group).map((key) => (key === 'appName' || key === 'appIcon' ? 'brand' : key.startsWith('upstreamClientVersionOverrides.') ? 'gateway-core' : Object.entries(settingsSectionFields).find(([, keys]) => keys.includes(key))?.[0])))
      for (const sectionKey of group.sectionKeys) expect(actual.has(sectionKey)).toBe(true)
    }
  })
})

describe('自定义判定', () => {
  it('数值偏离默认 → 自定义；相等 → 默认', () => {
    expect(isCustomValue('gatewayTextRawBodyLimitMegabytes', 32)).toBe(true)
    expect(isCustomValue('gatewayTextRawBodyLimitMegabytes', 16)).toBe(false)
  })

  it('0 = 无限语义仍参与自定义判定（默认非 0 的字段改 0 算自定义）', () => {
    expect(isCustomValue('userAiAccountLimit', 0)).toBe(true)
    expect(isCustomValue('userAiAccountLimit', 100)).toBe(false)
  })

  it('版本覆盖：非空 semver 自定义，空串默认', () => {
    expect(isCustomValue('upstreamClientVersionOverrides.codex', '0.159.3')).toBe(true)
    expect(isCustomValue('upstreamClientVersionOverrides.codex', '')).toBe(false)
  })

  it('组自定义计数随 baseline 偏离增长', () => {
    const access = defaultAccess()
    const limits = settingGroups.find((g) => g.key === 'request-limits')!
    expect(groupCustomCount(limits, access)).toBe(0)
    const tweaked = {
      ...access,
      baseline: (key: string) => (key === 'gatewayTextRawBodyLimitMegabytes' ? 32 : access.baseline(key)),
      isCustom: (key: string) => (key === 'gatewayTextRawBodyLimitMegabytes' ? true : access.isCustom(key))
    }
    expect(groupCustomCount(limits, tweaked)).toBe(1)
  })
})

describe('展示行生成', () => {
  it('默认库：各组行全部无 custom 标记，0 频次限流显示「不限」', () => {
    const access = defaultAccess()
    const limits = settingGroups.find((g) => g.key === 'request-limits')!
    const rows = limits.viewRows(access).flatMap((sg) => sg.rows)
    const rateRow = rows.find((r) => r.key === 'user-rate-limits')!
    expect(rateRow.value).toContain('每分钟 不限')
    expect(rateRow.custom).toBeFalsy()
    expect(rows.every((r) => !r.custom)).toBe(true)
  })

  it('自定义库：偏离行标 custom 并附默认对照', () => {
    const base = defaultAccess()
    const access: SettingsDisplayAccess = {
      baseline: (key) => (key === 'gatewayTextRawBodyLimitMegabytes' ? 32 : base.baseline(key)),
      isCustom: (key) => (key === 'gatewayTextRawBodyLimitMegabytes' ? true : base.isCustom(key))
    }
    const limits = settingGroups.find((g) => g.key === 'request-limits')!
    const row = limits.viewRows(access).flatMap((sg) => sg.rows).find((r) => r.key === 'gatewayTextRawBodyLimitMegabytes')!
    expect(row.value).toBe('32 MB')
    expect(row.custom).toBe(true)
    expect(row.defaultText).toBe('默认 16')
  })

  it('归并行任一成员自定义则整行 custom', () => {
    const base = defaultAccess()
    const access: SettingsDisplayAccess = {
      baseline: (key) => (key === 'accountHealthCheckIntervalHours' ? 6 : base.baseline(key)),
      isCustom: (key) => (key === 'accountHealthCheckIntervalHours' ? true : base.isCustom(key))
    }
    const scheduling = settingGroups.find((g) => g.key === 'scheduling')!
    const row = scheduling.viewRows(access).flatMap((sg) => sg.rows).find((r) => r.key === 'health-check')!
    expect(row.custom).toBe(true)
    expect(row.value).toContain('6 小时')
    expect(row.defaultText).toBe('默认 1 小时 · 10 分钟 · 3 次')
  })

  it('版本覆盖组：全内置一行；有覆盖逐行列出', () => {
    expect(settingGroups.find((g) => g.key === 'upstream-versions')!.viewRows(defaultAccess())[0].rows).toEqual([
      { key: 'upstream-all-default', label: '全部家族', value: '内置版本' }
    ])
    const base = defaultAccess()
    const access: SettingsDisplayAccess = {
      baseline: (key) => (key === 'upstreamClientVersionOverrides.codex' ? '0.159.3' : base.baseline(key)),
      isCustom: (key) => (key === 'upstreamClientVersionOverrides.codex' ? true : base.isCustom(key))
    }
    const rows = settingGroups.find((g) => g.key === 'upstream-versions')!.viewRows(access)[0].rows
    expect(rows).toEqual([{ key: 'upstreamClientVersionOverrides.codex', label: 'Codex Desktop', value: '0.159.3', custom: true }])
  })

  it('默认行不附默认对照，仅自定义行附（默认 X）', () => {
    const access = defaultAccess()
    const limits = settingGroups.find((g) => g.key === 'request-limits')!
    const rows = limits.viewRows(access).flatMap((sg) => sg.rows)
    for (const row of rows) {
      if (!row.custom) expect(row.defaultText).toBeUndefined()
    }
    const timeouts = settingGroups.find((g) => g.key === 'timeouts')!
    for (const sg of timeouts.viewRows(access)) {
      for (const row of sg.rows) {
        if (!row.custom) expect(row.defaultText).toBeUndefined()
      }
    }
  })

  it('品牌组：默认图标显示「默认图标」', () => {
    const brand = settingGroups.find((g) => g.key === 'brand')!
    const rows = brand.viewRows(defaultAccess())[0].rows
    expect(rows[0]).toMatchObject({ label: '系统名称', value: '聚合 AI' })
    expect(rows[1]).toMatchObject({ label: '系统图标', value: '默认图标', custom: false })
  })
})

describe('日志与审计组（log-audit）', () => {
  const group = () => settingGroups.find((g) => g.key === 'log-audit')!

  it('组级操作按钮存在（立即清理审计日志）', () => {
    expect(group().actions).toEqual([
      { key: 'audit-cleanup', label: '立即清理审计日志', confirm: '立即按当前保留策略执行一轮审计日志清理？' }
    ])
  })

  it('默认库：6 项归并为 3 行，全部无 custom 标记', () => {
    const rows = group().viewRows(defaultAccess()).flatMap((sg) => sg.rows)
    expect(rows.map((r) => r.key)).toEqual(['audit-retention', 'audit-hot-sample', 'operation-log'])
    expect(rows.every((r) => !r.custom)).toBe(true)
    expect(rows.find((r) => r.key === 'audit-retention')!.value).toBe('3 天 · 7 天')
    expect(rows.find((r) => r.key === 'audit-hot-sample')!.value).toBe('1 小时 · 1')
    expect(rows.find((r) => r.key === 'operation-log')!.value).toBe('365 天 · 100 条')
  })

  it('成功保留 0 显示「仅热窗」，整行 custom 并附默认对照', () => {
    const base = defaultAccess()
    const access: SettingsDisplayAccess = {
      baseline: (key) => (key === 'auditLogSuccessRetentionDays' ? 0 : base.baseline(key)),
      isCustom: (key) => (key === 'auditLogSuccessRetentionDays' ? true : base.isCustom(key))
    }
    const row = group().viewRows(access).flatMap((sg) => sg.rows).find((r) => r.key === 'audit-retention')!
    expect(row.value).toBe('仅热窗 · 7 天')
    expect(row.custom).toBe(true)
    expect(row.defaultText).toBe('默认 3 天 · 7 天')
  })

  it('采样率偏离默认时热窗行标 custom 并附默认对照', () => {
    const base = defaultAccess()
    const access: SettingsDisplayAccess = {
      baseline: (key) => (key === 'auditLogSuccessSampleRate' ? 0.1 : base.baseline(key)),
      isCustom: (key) => (key === 'auditLogSuccessSampleRate' ? true : base.isCustom(key))
    }
    const row = group().viewRows(access).flatMap((sg) => sg.rows).find((r) => r.key === 'audit-hot-sample')!
    expect(row.value).toBe('1 小时 · 0.1')
    expect(row.custom).toBe(true)
    expect(row.defaultText).toBe('默认 1 小时 · 1')
  })

  it('操作日志行偏离默认时标 custom 并附默认对照', () => {
    const base = defaultAccess()
    const access: SettingsDisplayAccess = {
      baseline: (key) => (key === 'operationLogRetentionDays' ? 90 : base.baseline(key)),
      isCustom: (key) => (key === 'operationLogRetentionDays' ? true : base.isCustom(key))
    }
    const row = group().viewRows(access).flatMap((sg) => sg.rows).find((r) => r.key === 'operation-log')!
    expect(row.value).toBe('90 天 · 100 条')
    expect(row.custom).toBe(true)
    expect(row.defaultText).toBe('默认 365 天 · 100 条')
  })
})
