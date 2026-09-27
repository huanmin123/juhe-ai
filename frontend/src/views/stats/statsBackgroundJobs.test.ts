import { describe, expect, it } from 'vitest'

import {
  backgroundJobNameText,
  backgroundJobRowClassName,
  backgroundJobStatusColor,
  backgroundJobStatusText,
  type BackgroundJobRow
} from './statsBackgroundJobs'

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

describe('后台任务名称映射', () => {
  it('jobregistry scheduled 全集逐个映射为中文文案', () => {
    expect(backgroundJobNameText('system-metrics-sample')).toBe('系统指标采样')
    expect(backgroundJobNameText('usage-stats-aggregation')).toBe('用量统计聚合')
    expect(backgroundJobNameText('account-list-availability-projection-maintenance')).toBe('账户列表可用性投影维护')
    expect(backgroundJobNameText('account-circuit-control-plane-maintenance')).toBe('账户熔断控制面维护')
    expect(backgroundJobNameText('data-retention-cleanup')).toBe('数据保留清理')
    expect(backgroundJobNameText('openai-oauth-access-token-refresh')).toBe('OpenAI OAuth 访问令牌刷新')
    expect(backgroundJobNameText('account-balance-stats-projection')).toBe('账户余额统计投影')
  })

  it('失败行返回整行高亮类，其余状态不加类', () => {
    expect(backgroundJobRowClassName({ status: 'failed' } as BackgroundJobRow)).toBe(
      'stats-background-job-row-failed'
    )
    expect(backgroundJobRowClassName({ status: 'completed' } as BackgroundJobRow)).toBe('')
    expect(backgroundJobRowClassName({ status: 'running' } as BackgroundJobRow)).toBe('')
  })

  it('未知任务名回退显示原文名', () => {
    expect(backgroundJobNameText('future-unknown-job')).toBe('future-unknown-job')
    expect(backgroundJobNameText('')).toBe('未知')
  })
})
