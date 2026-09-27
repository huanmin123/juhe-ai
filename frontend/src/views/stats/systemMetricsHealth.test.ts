import { describe, expect, it } from 'vitest'

import {
  gatewayHealthSection,
  healthConclusionColor,
  healthConclusionText,
  healthStatusLabel,
  healthValueText,
  jobsHealthSection,
  ownerModeText
} from './systemMetricsHealth'

describe('就绪项标签映射', () => {
  it('gateway 与 jobs 载荷的权威 key 映射为中文标签', () => {
    expect(healthStatusLabel('ready')).toBe('整体就绪')
    expect(healthStatusLabel('ownerReady')).toBe('Owner 就绪')
    expect(healthStatusLabel('ownerMode')).toBe('Owner 模式')
    expect(healthStatusLabel('auditLogReady')).toBe('审计日志')
    expect(healthStatusLabel('operationLogReady')).toBe('操作日志')
    expect(healthStatusLabel('j3bReady')).toBe('模型检查')
    expect(healthStatusLabel('jobsReady')).toBe('后台任务')
    expect(healthStatusLabel('sessionRetentionReady')).toBe('会话保留')
    expect(healthStatusLabel('accountCircuitRuntimeReady')).toBe('账户熔断运行时')
    expect(healthStatusLabel('tableMonitorReady')).toBe('表监控')
    expect(healthStatusLabel('proxyLatencyReady')).toBe('代理延迟探测')
  })

  it('未知 key 回退显示原 key', () => {
    expect(healthStatusLabel('futureReadinessKey')).toBe('futureReadinessKey')
  })
})

describe('ownerMode 与文本渲染', () => {
  it('active→主用、standby→备用、其他原样', () => {
    expect(ownerModeText('active')).toBe('主用')
    expect(ownerModeText('standby')).toBe('备用')
    expect(ownerModeText('drain')).toBe('drain')
    expect(ownerModeText(null)).toBe('-')
    expect(healthValueText('ownerMode', 'active')).toBe('主用')
  })

  it('非布尔值渲染文本：空值与对象回退', () => {
    expect(healthValueText('proxyLatencyLastError', 'boom')).toBe('boom')
    expect(healthValueText('proxyLatencyInputs', { a: 1 })).toBe('{"a":1}')
    expect(healthValueText('proxyLatencyLastError', null)).toBe('-')
  })
})

describe('进程卡结论判定', () => {
  it('全部布尔就绪=正常（绿）', () => {
    const section = gatewayHealthSection({ ready: true, ownerReady: true, ownerMode: 'active' })
    expect(section.conclusion).toBe('ok')
    expect(healthConclusionText('ok')).toBe('正常')
    expect(healthConclusionColor('ok')).toBe('success')
  })

  it('存在 false=部分未就绪（橙）', () => {
    const section = gatewayHealthSection({ ready: true, sessionRetentionReady: false })
    expect(section.conclusion).toBe('partial')
    expect(healthConclusionText('partial')).toBe('部分未就绪')
    expect(healthConclusionColor('partial')).toBe('warning')
  })

  it('jobs 不可达=不可达（红）且保留 reason', () => {
    const section = jobsHealthSection({ available: false, reason: 'jobs /health 状态码 503：down' })
    expect(section.conclusion).toBe('unreachable')
    expect(section.reason).toBe('jobs /health 状态码 503：down')
    expect(section.entries).toEqual([])
    expect(healthConclusionText('unreachable')).toBe('不可达')
    expect(healthConclusionColor('unreachable')).toBe('error')
  })

  it('jobs 可达时逐项透出 payload 并按布尔项判定结论', () => {
    const section = jobsHealthSection({ available: true, payload: { ready: true, tableMonitorReady: true } })
    expect(section.conclusion).toBe('ok')
    expect(section.entries).toEqual([['ready', true], ['tableMonitorReady', true]])
    const degraded = jobsHealthSection({ available: true, payload: { ready: false } })
    expect(degraded.conclusion).toBe('partial')
  })

  it('jobs 字段缺失时按不可达处理', () => {
    expect(jobsHealthSection(undefined).conclusion).toBe('unreachable')
  })
})
