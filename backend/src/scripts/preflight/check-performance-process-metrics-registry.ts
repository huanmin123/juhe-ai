import { setTimeout as delay } from 'node:timers/promises'

import {
  readPerformanceProcessEventLoopSamples,
  readPerformanceProcessMetricsRegistryTimeMs,
  stopPerformanceProcessMetricsPublisher
} from '../../shared/performance-process-metrics-registry.js'

const options = parseOptions(process.argv.slice(2))

try {
  if (options.printRedisTimeMs) {
    console.log(String(await readPerformanceProcessMetricsRegistryTimeMs()))
  } else {
    await waitForExpectedRoles(options)
  }
} finally {
  stopPerformanceProcessMetricsPublisher()
}

interface PreflightOptions {
  roles: string[]
  rolePids: ReadonlyMap<string, number>
  timeoutMs: number
  observedAfterMs?: number
  printRedisTimeMs: boolean
}

async function waitForExpectedRoles(options: PreflightOptions): Promise<void> {
  const deadlineAtMs = Date.now() + options.timeoutMs
  let lastObservedRoles: string[] = []
  let lastError: unknown
  while (Date.now() < deadlineAtMs) {
    try {
      const samples = await readPerformanceProcessEventLoopSamples()
      const eligibleSamples = options.observedAfterMs === undefined
        ? samples
        : samples.filter((sample) => Date.parse(sample.sampledAt) > options.observedAfterMs!)
      lastObservedRoles = [...new Set(eligibleSamples
        .filter((sample) => options.rolePids.get(sample.processRole) === undefined
          || options.rolePids.get(sample.processRole) === sample.processPid)
        .map((sample) => sample.processRole))].sort()
      const observed = new Set(lastObservedRoles)
      const missingRoles = options.roles.filter((role) => !observed.has(role))
      if (missingRoles.length === 0) {
        console.log(JSON.stringify({
          event: 'performance_process_metrics_registry_ready',
          expectedRoles: options.roles,
          pidBoundRoleCount: options.rolePids.size,
          observedAfterMs: options.observedAfterMs,
          observedRoleCount: lastObservedRoles.length
        }))
        return
      }
      lastError = new Error(`缺少角色: ${missingRoles.join(', ')}`)
    } catch (error) {
      lastError = error
    }
    await delay(Math.min(500, Math.max(1, deadlineAtMs - Date.now())))
  }
  const reason = lastError instanceof Error ? lastError.message : String(lastError ?? 'unknown')
  throw new Error(`performance 进程指标注册未就绪: ${reason}; fence 后已观察角色: ${lastObservedRoles.join(', ') || 'none'}`)
}

function parseOptions(args: string[]): PreflightOptions {
  const roles: string[] = []
  const rolePids = new Map<string, number>()
  let timeoutMs = 15_000
  let observedAfterMs: number | undefined
  let printRedisTimeMs = false
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index]
    if (argument === '--role') {
      const role = args[index + 1]?.trim()
      if (!role) throw new Error('--role 需要非空角色')
      roles.push(role)
      index += 1
      continue
    }
    if (argument === '--timeout-ms') {
      const value = Number(args[index + 1])
      if (!Number.isInteger(value) || value < 1_000 || value > 60_000) {
        throw new Error('--timeout-ms 必须是 1000..60000 的整数')
      }
      timeoutMs = value
      index += 1
      continue
    }
    if (argument === '--role-pid') {
      const value = args[index + 1]?.trim() ?? ''
      const separatorIndex = value.lastIndexOf('=')
      const role = value.slice(0, separatorIndex).trim()
      const processPid = Number(value.slice(separatorIndex + 1))
      if (separatorIndex < 1 || !role || !Number.isSafeInteger(processPid) || processPid <= 1) {
        throw new Error('--role-pid 必须是 role=pid，且 pid > 1')
      }
      const existingPid = rolePids.get(role)
      if (existingPid !== undefined && existingPid !== processPid) {
        throw new Error(`--role-pid 角色重复且 PID 冲突: ${role}`)
      }
      rolePids.set(role, processPid)
      index += 1
      continue
    }
    if (argument === '--observed-after-ms') {
      const value = Number(args[index + 1])
      if (!Number.isSafeInteger(value) || value <= 0) {
        throw new Error('--observed-after-ms 必须是正安全整数')
      }
      observedAfterMs = value
      index += 1
      continue
    }
    if (argument === '--print-redis-time-ms') {
      printRedisTimeMs = true
      continue
    }
    throw new Error(`未知参数: ${argument}`)
  }
  if (printRedisTimeMs && (roles.length > 0 || rolePids.size > 0 || observedAfterMs !== undefined)) {
    throw new Error('--print-redis-time-ms 不能与 --role、--role-pid 或 --observed-after-ms 同用')
  }
  if (!printRedisTimeMs && roles.length === 0) throw new Error('至少需要一个 --role')
  const uniqueRoles = [...new Set(roles)]
  for (const role of rolePids.keys()) {
    if (!uniqueRoles.includes(role)) throw new Error(`--role-pid 缺少对应 --role: ${role}`)
  }
  if (observedAfterMs !== undefined && rolePids.size === 0) {
    throw new Error('--observed-after-ms 必须为每个 --role 同时提供 --role-pid')
  }
  if (rolePids.size > 0) {
    for (const role of uniqueRoles) {
      if (!rolePids.has(role)) throw new Error(`启用 PID 绑定时每个 --role 都需要 --role-pid: ${role}`)
    }
  }
  return { roles: uniqueRoles, rolePids, timeoutMs, observedAfterMs, printRedisTimeMs }
}
