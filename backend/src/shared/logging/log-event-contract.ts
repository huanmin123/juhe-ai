import { formatShanghaiNow } from '../time-display.js'

export const LOG_EVENT_VERSION = 1 as const

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'
export type LogOutcome = 'success' | 'skipped' | 'expected_failure' | 'unexpected_failure' | 'aborted'
export type FailureClass = 'expected' | 'unexpected' | 'aborted' | 'infrastructure'

export interface LogEventEnvelope {
  time: string
  level: LogLevel
  version: number
  service: string
  role: string
  event: string
  traceId?: string
  requestId?: string
  jobId?: string
  parentId?: string
  outcome?: LogOutcome
  failureClass?: FailureClass
  stage?: string
  durationMs?: number
  startedOffsetMs?: number
  endedOffsetMs?: number
  environment?: string
  fields?: Record<string, unknown>
}

export interface LogEventInput extends Omit<LogEventEnvelope, 'time' | 'version'> {
  time?: string
}

export function createLogEventEnvelope(input: LogEventInput): LogEventEnvelope {
  validateRequired(input)
  if (input.startedOffsetMs !== undefined || input.endedOffsetMs !== undefined || input.durationMs !== undefined) {
    if (![input.startedOffsetMs, input.endedOffsetMs, input.durationMs].every((value) => Number.isFinite(value))) {
      throw new Error('日志耗时字段必须是有限数字')
    }
    if (input.startedOffsetMs! < 0 || input.endedOffsetMs! < input.startedOffsetMs! || input.endedOffsetMs! - input.startedOffsetMs! !== input.durationMs) {
      throw new Error('日志耗时字段必须满足 endedOffsetMs - startedOffsetMs = durationMs')
    }
  }
  if (input.failureClass && !['expected', 'unexpected', 'aborted', 'infrastructure'].includes(input.failureClass)) {
    throw new Error(`无效失败分类: ${input.failureClass}`)
  }
  return {
    time: input.time ?? formatShanghaiNow(),
    version: LOG_EVENT_VERSION,
    level: input.level,
    service: input.service,
    role: input.role,
    event: input.event,
    ...(input.traceId ? { traceId: input.traceId } : {}),
    ...(input.requestId ? { requestId: input.requestId } : {}),
    ...(input.jobId ? { jobId: input.jobId } : {}),
    ...(input.parentId ? { parentId: input.parentId } : {}),
    ...(input.outcome ? { outcome: input.outcome } : {}),
    ...(input.failureClass ? { failureClass: input.failureClass } : {}),
    ...(input.stage ? { stage: input.stage } : {}),
    ...(input.durationMs !== undefined ? { durationMs: input.durationMs } : {}),
    ...(input.startedOffsetMs !== undefined ? { startedOffsetMs: input.startedOffsetMs } : {}),
    ...(input.endedOffsetMs !== undefined ? { endedOffsetMs: input.endedOffsetMs } : {}),
    ...(input.environment ? { environment: input.environment } : {}),
    ...(input.fields ? { fields: input.fields } : {})
  }
}

function validateRequired(input: LogEventInput): void {
  for (const [name, value] of Object.entries(input)) {
    if (name === 'time' || name === 'fields' || value === undefined) continue
    if (typeof value === 'string' && !value.trim()) throw new Error(`日志字段不能为空: ${name}`)
  }
  if (!input.level || !input.service || !input.role || !input.event) throw new Error('日志事件缺少必填字段')
}
