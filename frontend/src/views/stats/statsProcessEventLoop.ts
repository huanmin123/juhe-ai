import type { SystemMetricsOverview } from '@/types/domain'

export interface ProcessEventLoopRow {
  processRole: string
  processPid: number | null
  latestSampleAvailable: boolean
  latestEventLoopLagMs: number | null
  latestProcessRssBytes: number | null
  latestProcessHeapUsedBytes: number | null
  latestProcessHeapTotalBytes: number | null
  latestSampledAt: string | null
  peakSampleAvailable: boolean
  peakEventLoopLagMs: number | null
  peakSampledAt: string | null
}

export const processEventLoopColumns = [
  { title: '进程', key: 'processRole', width: 116 },
  { title: 'PID', key: 'processPid', width: 90 },
  { title: 'RSS', key: 'processRss', width: 96 },
  { title: 'Heap', key: 'processHeap', width: 118 },
  { title: '最新延迟', key: 'latestLag', width: 104 },
  { title: '24小时峰值', key: 'peakLag', width: 112 },
  { title: '采样时间', key: 'sampledAt', width: 168 },
  { title: '状态', key: 'status', width: 86 }
]

const processEventLoopRoleOrder = new Map([
  ['server', 0],
  ['ingest-worker', 1],
  ['stats-worker', 2],
  ['ops-worker', 3],
  ['db-service', 4]
])

export function buildProcessEventLoopRows(metrics?: SystemMetricsOverview): ProcessEventLoopRow[] {
  const latestByRole = new Map((metrics?.processEventLoopLatestStatus ?? []).map((row) => [row.processRole, row]))
  const peakByRole = new Map((metrics?.processEventLoopPeakStatus ?? []).map((row) => [row.processRole, row]))
  const roles = [...new Set([...latestByRole.keys(), ...peakByRole.keys()])]
    .sort((left, right) => (processEventLoopRoleOrder.get(left) ?? 99) - (processEventLoopRoleOrder.get(right) ?? 99))

  return roles.map((processRole) => {
    const latest = latestByRole.get(processRole)
    const peak = peakByRole.get(processRole)
    return {
      processRole,
      processPid: latest?.processPid ?? peak?.processPid ?? null,
      latestSampleAvailable: latest?.sampleAvailable === true,
      latestEventLoopLagMs: latest?.eventLoopLagMs ?? null,
      latestProcessRssBytes: latest?.processRssBytes ?? null,
      latestProcessHeapUsedBytes: latest?.processHeapUsedBytes ?? null,
      latestProcessHeapTotalBytes: latest?.processHeapTotalBytes ?? null,
      latestSampledAt: latest?.sampledAt ?? null,
      peakSampleAvailable: peak?.sampleAvailable === true,
      peakEventLoopLagMs: peak?.eventLoopLagMs ?? null,
      peakSampledAt: peak?.sampledAt ?? null
    }
  })
}

export function hasProcessEventLoopRowSample(rows: ProcessEventLoopRow[]): boolean {
  return rows.some((item) => item.latestSampleAvailable || item.peakSampleAvailable)
}
