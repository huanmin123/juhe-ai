import type { GatewaySettings } from '../policy/account-error-policy.service.js'

export interface StreamReadPlan {
  phase: 'first_chunk' | 'active_stream' | 'no_circuit_breaker'
  timeoutMs?: number
  rawTimeoutMs?: number
  semanticResultTimeoutMs?: number
  streamLifetimeTimeoutMs?: number
  timeoutKind?: 'first_chunk' | 'upstream_activity' | 'semantic_result' | 'stream_lifetime'
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
    semanticResultReceived: boolean
    pendingProtocolEvent: boolean
    parserSkipped: boolean
  }
): StreamReadPlan {
  if (!settings.streamCircuitBreakerEnabled) {
    return {
      phase: 'no_circuit_breaker',
      timeoutMessage: '',
      deadlineExceeded: false
    }
  }

  const now = Date.now()
  const streamMaxLifetimeSeconds = Math.max(60, settings.streamMaxLifetimeSeconds)
  const streamLifetimeTimeoutMs = streamMaxLifetimeSeconds * 1000 - (now - startedAt)

  if (!status.waitingForFirstChunk || status.upstreamChunkReceived) {
    const streamIdleTimeoutSeconds = Math.max(1, settings.streamIdleTimeoutSeconds)
    const rawTimeoutMs = streamIdleTimeoutSeconds * 1000 - (now - status.lastUpstreamActivityAt)
    const semanticResultTimeoutSeconds = Math.max(1, settings.streamRequestTimeoutSeconds)
    const semanticResultTimeoutMs = semanticResultTimeoutSeconds * 1000 - (now - startedAt)
    if (
      !status.semanticResultReceived
      && !status.pendingProtocolEvent
      && !status.parserSkipped
      && semanticResultTimeoutMs <= rawTimeoutMs
      && semanticResultTimeoutMs <= streamLifetimeTimeoutMs
    ) {
      return {
        phase: 'active_stream',
        timeoutMs: semanticResultTimeoutMs,
        rawTimeoutMs,
        semanticResultTimeoutMs,
        streamLifetimeTimeoutMs,
        timeoutKind: 'semantic_result',
        timeoutMessage: streamSemanticResultTimeoutMessage(semanticResultTimeoutSeconds),
        deadlineExceeded: semanticResultTimeoutMs <= 0
      }
    }
    if (streamLifetimeTimeoutMs <= rawTimeoutMs) {
      return {
        phase: 'active_stream',
        timeoutMs: streamLifetimeTimeoutMs,
        rawTimeoutMs,
        semanticResultTimeoutMs: status.semanticResultReceived || status.pendingProtocolEvent || status.parserSkipped ? undefined : semanticResultTimeoutMs,
        streamLifetimeTimeoutMs,
        timeoutKind: 'stream_lifetime',
        timeoutMessage: streamMaxLifetimeTimeoutMessage(streamMaxLifetimeSeconds),
        deadlineExceeded: streamLifetimeTimeoutMs <= 0
      }
    }
    // Raw upstream activity remains the hard timeout while a protocol event is incomplete:
    // large or fragmented events can stay valid while bytes continue to arrive.
    return {
      phase: 'active_stream',
      timeoutMs: rawTimeoutMs,
      rawTimeoutMs,
      semanticResultTimeoutMs: status.semanticResultReceived || status.pendingProtocolEvent || status.parserSkipped ? undefined : semanticResultTimeoutMs,
      streamLifetimeTimeoutMs,
      timeoutKind: 'upstream_activity',
      timeoutMessage: streamIdleTimeoutMessage(streamIdleTimeoutSeconds),
      deadlineExceeded: rawTimeoutMs <= 0
    }
  }

  const firstChunkTimeoutSeconds = Math.max(1, settings.streamRequestTimeoutSeconds)
  const firstChunkTimeoutMs = firstChunkTimeoutSeconds * 1000 - (now - startedAt)
  if (streamLifetimeTimeoutMs <= firstChunkTimeoutMs) {
    return {
      phase: 'first_chunk',
      timeoutMs: streamLifetimeTimeoutMs,
      streamLifetimeTimeoutMs,
      timeoutKind: 'stream_lifetime',
      timeoutMessage: streamMaxLifetimeTimeoutMessage(streamMaxLifetimeSeconds),
      deadlineExceeded: streamLifetimeTimeoutMs <= 0
    }
  }
  return {
    phase: 'first_chunk',
    timeoutMs: firstChunkTimeoutMs,
    streamLifetimeTimeoutMs,
    timeoutKind: 'first_chunk',
    timeoutMessage: firstChunkTimeoutMessage(firstChunkTimeoutSeconds),
    deadlineExceeded: firstChunkTimeoutMs <= 0
  }
}

export function firstChunkTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式请求 ${timeoutSeconds}s 内未返回首段数据`
}

export function streamIdleTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式响应 ${timeoutSeconds}s 内未返回任何新数据`
}

export function streamSemanticResultTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式响应 ${timeoutSeconds}s 内未返回有效输出、失败或终止事件`
}

export function streamMaxLifetimeTimeoutMessage(timeoutSeconds: number): string {
  return `上游流式响应已达到最大存活时间 ${timeoutSeconds}s，已中断当前连接`
}
