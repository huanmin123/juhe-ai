import { describe, expect, it } from 'vitest'

import { backgroundJobStatusColor, backgroundJobStatusText } from './statsBackgroundJobs'

describe('后台任务状态映射', () => {
  it('真实值域 queued|running|completed|failed|skipped 映射为中文文案与语义色', () => {
    expect(backgroundJobStatusText('completed')).toBe('成功')
    expect(backgroundJobStatusColor('completed')).toBe('success')
    expect(backgroundJobStatusText('running')).toBe('运行中')
    expect(backgroundJobStatusColor('running')).toBe('processing')
    expect(backgroundJobStatusText('failed')).toBe('失败')
    expect(backgroundJobStatusColor('failed')).toBe('error')
    expect(backgroundJobStatusText('queued')).toBe('排队中')
    expect(backgroundJobStatusColor('queued')).toBe('default')
    expect(backgroundJobStatusText('skipped')).toBe('已跳过')
    expect(backgroundJobStatusColor('skipped')).toBe('default')
  })

  it('未知状态保留原文并降级为中性色', () => {
    expect(backgroundJobStatusText('mystery')).toBe('mystery')
    expect(backgroundJobStatusColor('mystery')).toBe('default')
    expect(backgroundJobStatusText('')).toBe('未知')
  })
})
