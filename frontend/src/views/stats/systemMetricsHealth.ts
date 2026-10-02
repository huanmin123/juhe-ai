import type { SystemMetricsHealthSnapshot } from '@/types/domain'

// 权威 key 清单来源（不得凭空新增）：
// - gateway 就绪载荷：backend-go/projects/gateway/cmd/juhe-ai-gateway/health_route.go readiness()
// - jobs /health 载荷：backend-go/projects/jobs/cmd/juhe-ai-jobs/main.go 健康监听 payload

/** 状态项标签（用户可见文案）。 */
const healthKeyLabels: Record<string, string> = {
  ready: '整体就绪',
  ownerReady: 'Owner 就绪',
  ownerMode: 'Owner 模式',
  auditLogReady: '审计日志',
  operationLogReady: '操作日志',
  j3bReady: '模型检查',
  jobsReady: '后台任务',
  sessionRetentionReady: '会话保留',
  accountCircuitRuntimeReady: '账户熔断运行时',
  runtimeLogOwnerHeld: '运行日志 Owner',
  tableMonitorReady: '表监控',
  accountHealthEnabled: '账户健康',
  accountHealthReady: '账户健康',
  accountBalanceEnabled: '账户余额',
  accountBalanceReady: '账户余额',
  proxyLatencyEnabled: '代理延迟探测',
  proxyLatencyReady: '代理延迟探测',
  proxyLatencyOwnerHeld: '代理延迟探测',
  workerEnabled: '后台 Worker',
  workerReady: '后台 Worker'
}

/** 诊断遥测标签（折叠区）。未命中回退原 key。 */
const diagnosticsKeyLabels: Record<string, string> = {
  proxyLatencyClaimed: '已认领周期',
  proxyLatencyDeferred: '已延后周期',
  proxyLatencyExecuted: '已执行探测',
  proxyLatencyExecutionFailures: '执行失败',
  proxyLatencyFailures: '探测失败',
  proxyLatencyInputs: '待处理输入',
  proxyLatencyLastCycleAt: '最近周期时间',
  proxyLatencyLastError: '最近错误',
  proxyLatencyLastSuccessAt: '最近成功时间',
  proxyLatencyPartial: '部分完成周期',
  proxyLatencyProcessed: '已处理输入',
  proxyLatencyReleaseFailures: '释放失败',
  proxyLatencySelected: '已选中账户',
  proxyLatencySkippedLeases: '跳过租约',
  proxyLatencyStarted: '已启动周期',
  proxyLatencyTarget: '目标账户数'
}

/**
 * 未启用域的一句话原因（与后端启动判定的依赖条件一致）：
 * - accountBalance / proxyLatency：存储仅支持 PostgreSQL，未配置 PG URL 时合法缺席
 *   （backend-go shared/platform/accountbalance/runtime_config.go、
 *   projects/jobs/internal/proxylatency/config.go）；
 * - accountHealth / worker：正常 owner 模式恒启用，false 只出现在蓝绿
 *   standby/drain 被动模式（jobs main.go passiveJobsHealthHandler）。
 */
const domainDisabledReasons: Record<string, string> = {
  accountHealth: '进程处于蓝绿备用/排水模式',
  accountBalance: '需 PostgreSQL，当前部署未配置',
  proxyLatency: '需 PostgreSQL，当前部署未配置',
  worker: '进程处于蓝绿备用/排水模式'
}

export type HealthConclusion = 'ok' | 'partial' | 'unreachable'

export interface HealthDiagnosticItem {
  key: string
  label: string
  value: string
}

export interface DisabledJobEntry {
  name: string
  reason: string
}

/** 按根因归并后的停用任务组：一个根因一行，组内只列任务名。 */
export interface DisabledJobGroup {
  cause: string
  jobs: string[]
}

/**
 * 停用任务按根因归并。根因标签从后端 reason 文本（jobs 进程写死的契约
 * 文案）提取；未命中已知根因的归「其他」，保留独立 reason 行不丢信息。
 */
export function groupDisabledJobsByCause(jobs: DisabledJobEntry[]): DisabledJobGroup[] {
  const groups = new Map<string, string[]>()
  for (const job of jobs) {
    let cause: string
    if (job.reason.includes('JUHE_AI_REDIS_STATE_URL')) {
      cause = '缺 Redis（JUHE_AI_REDIS_STATE_URL 未配置）'
    } else if (job.reason.includes('PostgreSQL')) {
      cause = '需 PostgreSQL'
    } else if (job.reason.includes('不独立注册') || job.reason.includes('并入')) {
      cause = 'SQLite 分支下并入其他任务执行'
    } else {
      cause = `其他：${job.reason}`
    }
    const names = groups.get(cause) ?? []
    names.push(job.name)
    groups.set(cause, names)
  }
  return Array.from(groups.entries()).map(([cause, names]) => ({ cause, jobs: names }))
}

/** 单个进程的健康展示模型：汇总优先，异常突出，遥测折叠。 */
export interface HealthProcessDisplay {
  conclusion: HealthConclusion
  reason?: string
  /** 就绪项标签（含未启用域之外的全部 true 布尔）。 */
  readyLabels: string[]
  /** 未启用项标签（含一句话原因，部署形态预期，中性灰，不算异常）。 */
  disabledLabels: string[]
  /** 真异常项标签（启用了但不就绪）。 */
  unhealthyLabels: string[]
  /** Owner 模式文案（如「主用」）；无则 undefined。 */
  ownerMode?: string
  /** 已停用任务明细（名字+原因）。 */
  disabledJobs: DisabledJobEntry[]
  /** 已停用任务按根因归并（一行一问题）。 */
  disabledJobGroups: DisabledJobGroup[]
  /** 运维遥测（诊断折叠区）。 */
  diagnostics: HealthDiagnosticItem[]
}

export function healthConclusionText(conclusion: HealthConclusion): string {
  if (conclusion === 'ok') return '正常'
  if (conclusion === 'partial') return '部分未就绪'
  return '不可达'
}

export function healthConclusionColor(conclusion: HealthConclusion): string {
  if (conclusion === 'ok') return 'success'
  if (conclusion === 'partial') return 'warning'
  return 'error'
}

/** 诊断项 key → 中文标签；未知 key 回退显示原 key。 */
export function healthDiagnosticLabel(key: string): string {
  return diagnosticsKeyLabels[key] ?? key
}

/** Owner 模式文本值：active→主用、standby→备用、其他原样。 */
export function ownerModeText(value: unknown): string {
  if (value === 'active') return '主用'
  if (value === 'standby') return '备用'
  if (value === null || value === undefined || value === '') return '-'
  return String(value)
}

function disabledJobEntries(value: unknown): DisabledJobEntry[] {
  if (!Array.isArray(value)) return []
  return value
    .map((item) => {
      const record = typeof item === 'object' && item !== null ? (item as { jobName?: unknown; reason?: unknown }) : {}
      const name = typeof record.jobName === 'string' && record.jobName ? record.jobName : undefined
      if (!name) return undefined
      const reason = typeof record.reason === 'string' && record.reason ? record.reason : '原因未提供'
      return { name, reason }
    })
    .filter((entry): entry is DisabledJobEntry => entry !== undefined)
}

/** 主区停用任务摘要：只报数量（明细可展开），避免长任务名撑坏汇总行。 */
export function workerDisabledJobsSummary(value: unknown): string {
  if (!Array.isArray(value)) {
    return value === null || value === undefined || value === '' ? '无' : String(value)
  }
  return value.length ? `共 ${value.length} 个` : '无'
}

/** worker 任务执行摘要：LastOutcome 统计 → 「共 N 个：成功 N（无异常）」。 */
function workerJobsSummary(value: unknown): string {
  if (!Array.isArray(value)) return '无任务明细'
  let failed = 0
  for (const item of value) {
    const outcome = typeof item === 'object' && item !== null ? (item as { LastOutcome?: unknown }).LastOutcome : undefined
    if (typeof outcome === 'string' && outcome && outcome !== 'success') failed += 1
  }
  return `共 ${value.length} 个：成功 ${value.length - failed}${failed ? ` / 异常 ${failed}` : '（无异常）'}`
}

/** 用量写入器计数摘要。 */
function workerUsageWriterSummary(value: unknown): string {
  if (typeof value !== 'object' || value === null) return '-'
  const record = value as { writtenRecords?: unknown; queueLength?: unknown; deadLetterCount?: unknown }
  return `已写入 ${record.writtenRecords ?? 0}，队列 ${record.queueLength ?? 0}，死信 ${record.deadLetterCount ?? 0}`
}

function scalarText(value: unknown): string {
  if (value === null || value === undefined || value === '') return '-'
  return String(value)
}

/** 键是否属于任一未启用域（域根前缀判定，覆盖 Ready/OwnerHeld/遥测计数器）。 */
function keyBelongsToDisabledDomain(key: string, domainDisabled: Set<string>): boolean {
  for (const domain of domainDisabled) {
    if (key.startsWith(domain)) return true
  }
  return false
}

/**
 * 把健康载荷解析为分级展示模型。
 *
 * 语义分级（核心设计约束）：
 * - 「未启用」（<域>Enabled=false）是部署形态的预期配置，归中性灰、不参与异常判定；
 * - 只有「已启用但不就绪」才算真异常（亮 warning）；
 * - 其余 true 项归就绪汇总（全绿时一行带过，异常时才是视觉焦点）。
 */
export function buildHealthProcessDisplay(entries: Array<[string, unknown]>): HealthProcessDisplay {
  const map = new Map(entries)
  const readyLabels: string[] = []
  const disabledLabels: string[] = []
  const unhealthyLabels: string[] = []
  let ownerMode: string | undefined
  const diagnostics: HealthDiagnosticItem[] = []

  // 域根 → Enabled key 对（accountHealthEnabled / accountBalanceEnabled / proxyLatencyEnabled / workerEnabled）
  const domainEnabledKeys = new Set(['accountHealthEnabled', 'accountBalanceEnabled', 'proxyLatencyEnabled', 'workerEnabled'])
  const domainDisabled = new Set<string>()
  for (const key of domainEnabledKeys) {
    if (map.get(key) === false) {
      const domain = key.replace(/Enabled$/, '')
      domainDisabled.add(domain)
      const label = healthKeyLabels[key] ?? key
      disabledLabels.push(`${label}（${domainDisabledReasons[domain] ?? '当前部署未启用'}）`)
    }
  }

  for (const [key, value] of entries) {
    // Enabled 键一律不进布尔分级：false 的已在 disabledLabels（部署预期），
    // true 的无独立信息量（就绪由对应 Ready 键表达）
    if (domainEnabledKeys.has(key)) continue

    // 域未启用：域内一切键（Ready/OwnerHeld 的 false 属预期；遥测计数器如
    // proxyLatency* 17 项恒为 0）都不进任何分级，避免"未启用"被误读为异常
    if (keyBelongsToDisabledDomain(key, domainDisabled)) continue

    if (key === 'ownerMode') {
      ownerMode = ownerModeText(value)
      continue
    }
    if (key === 'workerDisabledJobs' || (key === 'worker' && typeof value === 'object' && value !== null)) {
      continue // worker 域下面单独拆
    }
    if (healthKeyLabels[key] !== undefined) {
      const label = healthKeyLabels[key]
      if (typeof value === 'boolean') {
        if (value) {
          if (!readyLabels.includes(label)) readyLabels.push(label)
        } else if (!unhealthyLabels.includes(label)) {
          unhealthyLabels.push(label)
        }
      }
      continue
    }
    diagnostics.push({ key, label: healthDiagnosticLabel(key), value: scalarText(value) })
  }

  const workerRaw = map.get('worker')
  let disabledJobs: DisabledJobEntry[] = []
  if (typeof workerRaw === 'object' && workerRaw !== null) {
    const record = workerRaw as Record<string, unknown>
    disabledJobs = disabledJobEntries(record.workerDisabledJobs)
    diagnostics.push(
      { key: 'worker.driver', label: 'Worker 驱动', value: scalarText(record.workerDriver) },
      { key: 'worker.jobs', label: '后台任务执行', value: workerJobsSummary(record.workerJobs) },
      { key: 'worker.wiredJobs', label: '已注册任务数', value: Array.isArray(record.workerWiredJobs) ? String(record.workerWiredJobs.length) : '-' },
      { key: 'worker.usageWriter', label: '用量写入器', value: workerUsageWriterSummary(record.workerUsageWriterRuntime) }
    )
  } else if (map.has('worker')) {
    diagnostics.push({ key: 'worker', label: 'Worker 状态', value: scalarText(workerRaw) })
  }

  const conclusion: HealthConclusion = unhealthyLabels.length > 0 ? 'partial' : 'ok'
  return {
    conclusion,
    readyLabels,
    disabledLabels,
    unhealthyLabels,
    ownerMode,
    disabledJobs,
    disabledJobGroups: groupDisabledJobsByCause(disabledJobs),
    diagnostics
  }
}

/** Gateway 进程卡。 */
export function gatewayHealthDisplay(payload: Record<string, unknown> | undefined): HealthProcessDisplay {
  return buildHealthProcessDisplay(Object.entries(payload ?? {}))
}

/** Jobs 进程卡：不可达时 conclusion=unreachable 并保留 reason。 */
export function jobsHealthDisplay(jobs: SystemMetricsHealthSnapshot['jobs'] | undefined): HealthProcessDisplay {
  if (!jobs || !jobs.available) {
    return {
      conclusion: 'unreachable',
      reason: jobs?.reason,
      readyLabels: [],
      disabledLabels: [],
      unhealthyLabels: [],
      disabledJobs: [],
      disabledJobGroups: [],
      diagnostics: []
    }
  }
  return buildHealthProcessDisplay(Object.entries(jobs.payload ?? {}))
}
