import { describe, expect, it } from 'vitest'

import {
  gatewayHealthDisplay,
  healthConclusionColor,
  healthConclusionText,
  jobsHealthDisplay,
  ownerModeText,
  workerDisabledJobsSummary
} from './systemMetricsHealth'

describe('结论 tag 文案与配色', () => {
  it('ok/partial/unreachable 三态', () => {
    expect(healthConclusionText('ok')).toBe('正常')
    expect(healthConclusionText('partial')).toBe('部分未就绪')
    expect(healthConclusionText('unreachable')).toBe('不可达')
    expect(healthConclusionColor('ok')).toBe('success')
    expect(healthConclusionColor('partial')).toBe('warning')
    expect(healthConclusionColor('unreachable')).toBe('error')
  })
})

describe('ownerMode 文本', () => {
  it('active→主用、standby→备用、其他原样', () => {
    expect(ownerModeText('active')).toBe('主用')
    expect(ownerModeText('standby')).toBe('备用')
    expect(ownerModeText('drain')).toBe('drain')
    expect(ownerModeText(null)).toBe('-')
  })
})

describe('进程健康分级（信息架构核心契约）', () => {
  it('全就绪：结论正常，就绪项汇总为中文标签', () => {
    const display = gatewayHealthDisplay({
      ready: true,
      ownerReady: true,
      ownerMode: 'active',
      auditLogReady: true,
      operationLogReady: true,
      j3bReady: true,
      jobsReady: true,
      sessionRetentionReady: true,
      accountCircuitRuntimeReady: true,
      tableMonitorReady: true
    })
    expect(display.conclusion).toBe('ok')
    expect(display.ownerMode).toBe('主用')
    expect(display.readyLabels).toContain('审计日志')
    expect(display.readyLabels).toContain('账户熔断运行时')
    expect(display.unhealthyLabels).toEqual([])
    expect(display.disabledLabels).toEqual([])
  })

  it('未启用的功能域归中性灰，不算异常（部署形态预期 ≠ 故障）', () => {
    const display = gatewayHealthDisplay({
      ready: true,
      accountHealthEnabled: false,
      accountHealthReady: false,
      accountBalanceEnabled: false,
      accountBalanceReady: false,
      tableMonitorReady: true
    })
    expect(display.conclusion).toBe('ok')
    expect(display.disabledLabels).toEqual([
      '账户健康（进程处于蓝绿备用/排水模式）',
      '账户余额（需 PostgreSQL，当前部署未配置）'
    ])
    expect(display.unhealthyLabels).toEqual([])
    expect(display.readyLabels).toEqual(['整体就绪', '表监控'])
  })

  it('已启用但不就绪才是真异常：结论部分未就绪，异常项突出', () => {
    const display = gatewayHealthDisplay({
      ready: true,
      tableMonitorReady: false,
      accountHealthEnabled: true,
      accountHealthReady: false
    })
    expect(display.conclusion).toBe('partial')
    expect(display.unhealthyLabels).toEqual(['表监控', '账户健康'])
  })

  it('worker 域：Enabled=false 时域内 Ready/OwnerHeld 均归未启用（带原因）', () => {
    const display = jobsHealthDisplay({
      available: true,
      payload: { ready: true, workerEnabled: false, workerReady: false }
    })
    expect(display.conclusion).toBe('ok')
    expect(display.disabledLabels).toEqual(['后台 Worker（进程处于蓝绿备用/排水模式）'])
  })
})

describe('worker 大对象拆解（诊断区结构化，绝无 JSON 原文）', () => {
  // workerDisabledJobs 取隔离 jobs /health 的真实 6 条（三类根因：Redis/PG/合并注册）
  const display = jobsHealthDisplay({
    available: true,
    payload: {
      ready: true,
      proxyLatencyEnabled: false,
      proxyLatencyClaimed: 3,
      proxyLatencyLastCycleAt: '2026-10-01T13:00:00Z',
      someFutureTelemetry: 7,
      worker: {
        workerEnabled: true,
        workerDriver: 'sqlite',
        workerDisabledJobs: [
          { jobName: 'ai-performance-summary-windows-refresh', reason: '默认/SQLite 分支不独立注册（Node background-jobs.ts:310 该 stage 并入 usage-rank-snapshots-refresh）' },
          { jobName: 'account-balance-stats-projection', reason: 'J2 快照仅存在于 PostgreSQL（juhe_jobs.account_balance_snapshots）' },
          { jobName: 'account-circuit-control-plane-maintenance', reason: '缺 JUHE_AI_REDIS_STATE_URL（账户电路运行态为 Redis 单实现）' },
          { jobName: 'account-circuit-recovery', reason: '同上：缺 JUHE_AI_REDIS_STATE_URL' },
          { jobName: 'account-list-availability-projection-maintenance', reason: 'Node PostgreSQL-only 物化器：databaseDriver != postgres 时不注册' },
          { jobName: 'normal-route-speed-first-recovery-probe', reason: '缺 JUHE_AI_REDIS_STATE_URL（速度优先降级运行态为 Redis 单实现）' }
        ],
        workerJobs: [
          { Name: 'a', LastOutcome: 'success' },
          { Name: 'b', LastOutcome: 'success' },
          { Name: 'c', LastOutcome: 'failed' }
        ],
        workerWiredJobs: ['a', 'b', 'c'],
        workerUsageWriterRuntime: { writtenRecords: 12, queueLength: 0, deadLetterCount: 0 }
      }
    }
  })

  it('停用任务摘要只报数量，明细保留名字与原因', () => {
    expect(workerDisabledJobsSummary([{ jobName: 'a' }, { jobName: 'b' }])).toBe('共 2 个')
    expect(workerDisabledJobsSummary([])).toBe('无')
    expect(workerDisabledJobsSummary(null)).toBe('无')
    expect(workerDisabledJobsSummary('legacy')).toBe('legacy')
    expect(display.disabledJobs).toHaveLength(6)
    expect(display.disabledJobs[0]).toEqual({
      name: 'ai-performance-summary-windows-refresh',
      reason: expect.stringContaining('不独立注册')
    })
  })

  it('停用任务按根因归并：一行一问题，组内只列任务名', () => {
    expect(display.disabledJobGroups).toEqual([
      {
        cause: 'SQLite 分支下并入其他任务执行',
        jobs: ['ai-performance-summary-windows-refresh']
      },
      {
        cause: '需 PostgreSQL',
        jobs: ['account-balance-stats-projection', 'account-list-availability-projection-maintenance']
      },
      {
        cause: '缺 Redis（JUHE_AI_REDIS_STATE_URL 未配置）',
        jobs: ['account-circuit-control-plane-maintenance', 'account-circuit-recovery', 'normal-route-speed-first-recovery-probe']
      }
    ])
  })

  it('未知根因归「其他」且不合并不同原因', () => {
    const other = jobsHealthDisplay({
      available: true,
      payload: {
        ready: true,
        worker: {
          workerEnabled: true,
          workerDisabledJobs: [
            { jobName: 'x', reason: '某种从未见过的问题' },
            { jobName: 'y', reason: '另一种不同的问题' }
          ]
        }
      }
    })
    expect(other.disabledJobGroups).toHaveLength(2)
    expect(other.disabledJobGroups[0].cause).toBe('其他：某种从未见过的问题')
    expect(other.disabledJobGroups[1].jobs).toEqual(['y'])
  })

  it('未启用域的遥测一并过滤：proxyLatency* 计数器恒 0，不进诊断区也不算异常', () => {
    const keys = display.diagnostics.map((item) => item.key)
    expect(keys).not.toContain('proxyLatencyClaimed')
    expect(keys).not.toContain('proxyLatencyLastCycleAt')
    expect(display.disabledLabels).toEqual(['代理延迟探测（需 PostgreSQL，当前部署未配置）'])
  })

  it('启用域的遥测照常进诊断区（中文标签）', () => {
    const enabled = jobsHealthDisplay({
      available: true,
      payload: { ready: true, proxyLatencyEnabled: true, proxyLatencyClaimed: 3, proxyLatencyLastCycleAt: '2026-10-01T13:00:00Z' }
    })
    const labels = enabled.diagnostics.map((item) => item.label)
    expect(labels).toContain('已认领周期')
    expect(labels).toContain('最近周期时间')
  })

  it('诊断区为语义行：未知遥测原 key + worker 结构化摘要，无 JSON、无停用任务长文本', () => {
    const labels = display.diagnostics.map((item) => item.label)
    expect(labels).toContain('someFutureTelemetry')
    expect(labels).toContain('Worker 驱动')
    expect(labels).toContain('后台任务执行')
    expect(labels).toContain('已注册任务数')
    expect(labels).toContain('用量写入器')
    // 停用任务明细由独立折叠区按根因承载，诊断区不再重复铺长文本
    expect(labels).not.toContain('已停用任务原因')
    const byLabel = new Map(display.diagnostics.map((item) => [item.label, item.value]))
    expect(byLabel.get('Worker 驱动')).toBe('sqlite')
    expect(byLabel.get('后台任务执行')).toBe('共 3 个：成功 2 / 异常 1')
    expect(byLabel.get('已注册任务数')).toBe('3')
    expect(byLabel.get('用量写入器')).toBe('已写入 12，队列 0，死信 0')
    expect(JSON.stringify(display.diagnostics)).not.toContain('"workerJobs"')
  })

  it('遥测计数器不影响结论判定', () => {
    expect(display.conclusion).toBe('ok')
  })
})

describe('jobs 不可达', () => {
  it('conclusion=unreachable 保留 reason，不产出分级内容', () => {
    const display = jobsHealthDisplay({ available: false, reason: 'jobs /health 状态码 503：down' })
    expect(display.conclusion).toBe('unreachable')
    expect(display.reason).toBe('jobs /health 状态码 503：down')
    expect(display.readyLabels).toEqual([])
  })

  it('jobs 字段缺失按不可达处理', () => {
    expect(jobsHealthDisplay(undefined).conclusion).toBe('unreachable')
  })
})
