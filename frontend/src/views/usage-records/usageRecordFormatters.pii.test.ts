import { describe, expect, it } from 'vitest'

import {
  usageRecordHasPiiHit,
  usageRecordPiiHitDetail,
  usageRecordPiiHitTotal
} from './usageRecordFormatters'

describe('防护次数口径（敏标记 / 脱敏明细）', () => {
  it('多规则计数求和', () => {
    expect(usageRecordPiiHitTotal({ piiHits: { PHONE: 2, SECRET: 1 } })).toBe(3)
  })

  it('非法值与零值不计入', () => {
    expect(
      usageRecordPiiHitTotal({ piiHits: { PHONE: 0, EMAIL: -1, IP: Number.NaN } })
    ).toBe(0)
    expect(usageRecordPiiHitTotal({ piiHits: undefined })).toBe(0)
    expect(usageRecordPiiHitTotal({})).toBe(0)
  })

  it('敏标记判定：总命中 >0 才显示', () => {
    expect(usageRecordHasPiiHit({ piiHits: { PHONE: 1 } })).toBe(true)
    expect(usageRecordHasPiiHit({ piiHits: {} })).toBe(false)
    expect(usageRecordHasPiiHit({ piiHits: undefined })).toBe(false)
  })

  it('明细按 label 列出且过滤非法计数', () => {
    expect(usageRecordPiiHitDetail({ piiHits: { PHONE: 2, SECRET: 1 } })).toBe(
      'PHONE×2、SECRET×1'
    )
    expect(usageRecordPiiHitDetail({ piiHits: { PHONE: 0 } })).toBe('无脱敏命中')
    expect(usageRecordPiiHitDetail({ piiHits: undefined })).toBe('无脱敏命中')
  })
})
