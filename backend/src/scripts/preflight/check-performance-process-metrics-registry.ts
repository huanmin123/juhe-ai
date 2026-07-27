import { setTimeout as delay } from 'node:timers/promises'

import {
  readPerformanceProcessEventLoopSamples,
  stopPerformanceProcessMetricsPublisher
} from '../../shared/performance-process-metrics-registry.js'

const options = parseOptions(process.argv.slice(2))
const deadlineAtMs = Date.now() + options.timeoutMs
let lastObservedRoles: string[] = []
let lastError: unknown

try {
  while (Date.now() < deadlineAtMs) {
    try {
      const samples = await readPerformanceProcessEventLoopSamples()
      lastObservedRoles = [...new Set(samples.map((sample) => sample.processRole))].sort()
      const observed = new Set(lastObservedRoles)
      const missingRoles = options.roles.filter((role) => !observed.has(role))
      if (missingRoles.length === 0) {
        console.log(JSON.stringify({
          event: 'performance_process_metrics_registry_ready',
          expectedRoles: options.roles,
          observedRoleCount: lastObservedRoles.length
        }))
        process.exitCode = 0
        break
      }
      lastError = new Error(`缺少角色: ${missingRoles.join(', ')}`)
    } catch (error) {
      lastError = error
    }
    await delay(Math.min(500, Math.max(1, deadlineAtMs - Date.now())))
  }

  if (process.exitCode !== 0) {
    const reason = lastError instanceof Error ? lastError.message : String(lastError ?? 'unknown')
    throw new Error(`performance 进程指标注册未就绪: ${reason}; 已观察角色: ${lastObservedRoles.join(', ') || 'none'}`)
  }
} finally {
  stopPerformanceProcessMetricsPublisher()
}

function parseOptions(args: string[]): { roles: string[]; timeoutMs: number } {
  const roles: string[] = []
  let timeoutMs = 15_000
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
    throw new Error(`未知参数: ${argument}`)
  }
  if (roles.length === 0) throw new Error('至少需要一个 --role')
  return { roles: [...new Set(roles)], timeoutMs }
}
