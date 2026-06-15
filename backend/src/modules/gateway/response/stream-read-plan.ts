import type { GatewaySettings } from '../policy/account-error-policy.service.js'

export interface StreamReadPlan {
  phase: 'first_chunk' | 'active_stream' | 'no_circuit_breaker'
  timeoutMs?: number
  rawTimeoutMs?: number
  timeoutKind?: 'first_chunk' | 'upstream_activity'
  timeoutMessage: string
  deadlineExceeded: boolean
}

export function buildStreamReadPlan(
  settings: GatewaySettings,
  startedAt: number,
  status: {
    waitingForFirstChunk: boolean
    lastUpstreamActivityAt: number
    lastSseEventActivityAt?: number
    upstreamChunkReceived: boolean
  }
): StreamReadPlan {
  if (!settings.streamCircuitBreakerEnabled) {
    return {
      phase: 'no_circuit_breaker',
      timeoutMessage: '',
      deadlineExceeded: false
    }
  }

  if (!status.waitingForFirstChunk || status.upstreamChunkReceived) {
    const streamIdleTimeoutSeconds = Math.max(1, settings.streamIdleTimeoutSeconds)
    const now = Date.now()
    const rawTimeoutMs = streamIdleTimeoutSeconds * 1000 - (now - status.lastUpstreamActivityAt)
    // Raw upstream activity is the hard timeout. Incomplete SSE events are diagnostic only:
    // large or fragmented events can stay valid while bytes continue to arrive.
    return {
      phase: 'active_stream',
      timeoutMs: rawTimeoutMs,
      rawTimeoutMs,
      timeoutKind: 'upstream_activity',
      timeoutMessage: streamIdleTimeoutMessage(streamIdleTimeoutSeconds),
      deadlineExceeded: rawTimeoutMs <= 0
    }
  }

  const firstChunkTimeoutSeconds = Math.max(1, settings.streamRequestTimeoutSeconds)
  const timeoutMs = firstChunkTimeoutSeconds * 1000 - (Date.now() - startedAt)
  return {
    phase: 'first_chunk',
    timeoutMs,
    timeoutKind: 'first_chunk',
    timeoutMessage: firstChunkTimeoutMessage(firstChunkTimeoutSeconds),
    deadlineExceeded: timeoutMs <= 0
  }
}

export function firstChunkTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式请求 ${timeoutSeconds}s 内未返回首段数据`
}

export function streamIdleTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式响应 ${timeoutSeconds}s 内未返回任何新数据`
}
