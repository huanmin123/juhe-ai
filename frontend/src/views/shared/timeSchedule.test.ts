import { describe, expect, it } from 'vitest'

import { invertTimeScheduleWindows, peakWindowsToTimeSchedulePreset, weekdayOptions } from './timeSchedule'

const allDays = [1, 2, 3, 4, 5, 6, 7]

const deepSeekPeakWindows = {
  timezone: 'Asia/Shanghai',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5], start: '09:00', end: '12:00' },
    { daysOfWeek: [1, 2, 3, 4, 5], start: '14:00', end: '18:00' }
  ]
}

describe('invertTimeScheduleWindows', () => {
  it('全周单段高峰取反为单个全周反向时段（段终点 = 高峰起点）', () => {
    const inverted = invertTimeScheduleWindows([{ daysOfWeek: allDays, start: '22:00', end: '23:55' }])
    expect(inverted).toEqual([{ daysOfWeek: allDays, start: '00:00', end: '22:00' }])
  })

  it('全周跨午夜高峰取反为每天的低谷段', () => {
    // 每天 23:00-次日 06:00 高峰 → 反向每天 06:00-23:00
    const inverted = invertTimeScheduleWindows([{ daysOfWeek: allDays, start: '23:00', end: '06:00' }])
    expect(inverted).toEqual([{ daysOfWeek: allDays, start: '06:00', end: '23:00' }])
  })

  it('全天窗口取反无剩余时段时返回空', () => {
    const inverted = invertTimeScheduleWindows([{ daysOfWeek: allDays, start: '00:00', end: '23:59' }])
    expect(inverted).toEqual([])
  })

  it('跨午夜高峰只覆盖部分天时反向按天生成', () => {
    // 仅周一 23:00-次日 06:00：周一反向 00:00-23:00；周二反向 06:00-23:59；其余天全天
    const inverted = invertTimeScheduleWindows([{ daysOfWeek: [1], start: '23:00', end: '06:00' }])
    expect(inverted).toEqual([
      { daysOfWeek: [1], start: '00:00', end: '23:00' },
      { daysOfWeek: [3, 4, 5, 6, 7], start: '00:00', end: '23:59' },
      { daysOfWeek: [2], start: '06:00', end: '23:59' }
    ])
  })

  it('工作日双峰取反为三段 + 周末全天', () => {
    const workdays = [1, 2, 3, 4, 5]
    const inverted = invertTimeScheduleWindows([
      { daysOfWeek: workdays, start: '09:00', end: '12:00' },
      { daysOfWeek: workdays, start: '14:00', end: '18:00' }
    ])
    expect(inverted).toEqual([
      { daysOfWeek: workdays, start: '00:00', end: '09:00' },
      { daysOfWeek: [6, 7], start: '00:00', end: '23:59' },
      { daysOfWeek: workdays, start: '12:00', end: '14:00' },
      { daysOfWeek: workdays, start: '18:00', end: '23:59' }
    ])
  })

  it('短于 15 分钟的零头段丢弃', () => {
    // 高峰 22:00-23:55 的反向尾段 23:55-24:00 只有 5 分钟，不生成
    const inverted = invertTimeScheduleWindows([{ daysOfWeek: allDays, start: '22:00', end: '23:55' }])
    expect(inverted).toHaveLength(1)
    expect(inverted[0].end).toBe('22:00')
  })

  it('不同天的高峰不同时间时按天分组生成', () => {
    const inverted = invertTimeScheduleWindows([
      { daysOfWeek: [1], start: '12:00', end: '14:00' },
      { daysOfWeek: [2], start: '10:00', end: '16:00' }
    ])
    expect(inverted).toEqual([
      { daysOfWeek: [1], start: '00:00', end: '12:00' },
      { daysOfWeek: [2], start: '00:00', end: '10:00' },
      { daysOfWeek: [3, 4, 5, 6, 7], start: '00:00', end: '23:59' },
      { daysOfWeek: [1], start: '14:00', end: '23:59' },
      { daysOfWeek: [2], start: '16:00', end: '23:59' }
    ])
  })

  it('跨午夜高峰只覆盖部分天时正确展开次日', () => {
    // 仅周五 22:00-02:00：周五 [1320,1440) + 周六 [0,120) 被覆盖；
    // 周日全天反向与周一~周四同形，合并进同一行
    const inverted = invertTimeScheduleWindows([{ daysOfWeek: [5], start: '22:00', end: '02:00' }])
    expect(inverted).toEqual([
      { daysOfWeek: [1, 2, 3, 4, 7], start: '00:00', end: '23:59' },
      { daysOfWeek: [5], start: '00:00', end: '22:00' },
      { daysOfWeek: [6], start: '02:00', end: '23:59' }
    ])
  })
})

describe('peakWindowsToTimeSchedulePreset', () => {
  it('把供应商 peakWindows 转为待反向的快捷预设（label/description/时区联动目录）', () => {
    const preset = peakWindowsToTimeSchedulePreset('DeepSeek', deepSeekPeakWindows)
    expect(preset.key).toBe('provider_peak_DeepSeek')
    expect(preset.timezone).toBe('Asia/Shanghai')
    expect(preset.invertOnApply).toBe(true)
    expect(preset.label).toBe('DeepSeek 低峰时段')
    expect(preset.windows).toEqual(deepSeekPeakWindows.windows)
    expect(preset.windows).not.toBe(deepSeekPeakWindows.windows)
  })

  it('DeepSeek 预设反向生成结果符合用户口径：工作日三段 + 周末全天', () => {
    const preset = peakWindowsToTimeSchedulePreset('DeepSeek', deepSeekPeakWindows)
    const inverted = invertTimeScheduleWindows(preset.windows)
    expect(inverted).toEqual([
      { daysOfWeek: [1, 2, 3, 4, 5], start: '00:00', end: '09:00' },
      { daysOfWeek: [6, 7], start: '00:00', end: '23:59' },
      { daysOfWeek: [1, 2, 3, 4, 5], start: '12:00', end: '14:00' },
      { daysOfWeek: [1, 2, 3, 4, 5], start: '18:00', end: '23:59' }
    ])
  })

  it('weekdayOptions 保持七天完整（反向算法依赖 1..7 闭环）', () => {
    expect(weekdayOptions.map((item) => item.value)).toEqual([1, 2, 3, 4, 5, 6, 7])
  })
})
