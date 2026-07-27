import type { GatewayTimeoutProfile } from '../policy/timeout-profile.js'

export interface StreamReadPlan {
  phase: 'first_chunk' | 'active_stream'
  timeoutMs: number
  rawTimeoutMs?: number
  semanticResultTimeoutMs?: number
  streamLifetimeTimeoutMs?: number
  timeoutKind: 'first_chunk' | 'upstream_activity' | 'semantic_result' | 'stream_lifetime'
  timeoutMessage: string
  deadlineExceeded: boolean
}

export function buildGatewayStreamReadPlan(
  profile: GatewayTimeoutProfile,
  startedAt: number,
  status: Parameters<typeof buildStreamReadPlan>[2],
  now = Date.now()
): StreamReadPlan | undefined {
  if (profile.timeoutsDisabled !== true) {
    return buildStreamReadPlan(profile, startedAt, status, now)
  }
  if (status.waitingForFirstChunk && !status.upstreamChunkReceived) {
    return undefined
  }
  const rawTimeoutMs = profile.idleTimeoutMs - (now - status.lastUpstreamActivityAt)
  return {
    phase: 'active_stream',
    timeoutMs: rawTimeoutMs,
    rawTimeoutMs,
    timeoutKind: 'upstream_activity',
    timeoutMessage: streamIdleTimeoutMessage(timeoutSeconds(profile.idleTimeoutMs)),
    deadlineExceeded: rawTimeoutMs <= 0
  }
}

export function buildStreamReadPlan(
  profile: GatewayTimeoutProfile,
  startedAt: number,
  status: {
    waitingForFirstChunk: boolean
    lastUpstreamActivityAt: number
    lastSseEventActivityAt?: number
    upstreamChunkReceived: boolean
    semanticResultReceived: boolean
    pendingProtocolEvent: boolean
    parserSkipped: boolean
  },
  now = Date.now()
): StreamReadPlan {
  const streamLifetimeTimeoutMs = status.semanticResultReceived
    ? undefined
    : profile.uncommittedAttemptMaxLifetimeMs - (now - startedAt)

  if (!status.waitingForFirstChunk || status.upstreamChunkReceived) {
    const rawTimeoutMs = profile.idleTimeoutMs - (now - status.lastUpstreamActivityAt)
    const semanticResultStartedAt = status.lastSseEventActivityAt ?? startedAt
    const semanticResultTimeoutMs = profile.firstResponseTimeoutMs - (now - semanticResultStartedAt)
    if (
      !status.semanticResultReceived
      && !status.pendingProtocolEvent
      && !status.parserSkipped
      && semanticResultTimeoutMs <= rawTimeoutMs
      && semanticResultTimeoutMs <= (streamLifetimeTimeoutMs ?? Number.POSITIVE_INFINITY)
    ) {
      return {
        phase: 'active_stream',
        timeoutMs: semanticResultTimeoutMs,
        rawTimeoutMs,
        semanticResultTimeoutMs,
        streamLifetimeTimeoutMs,
        timeoutKind: 'semantic_result',
        timeoutMessage: streamSemanticResultTimeoutMessage(timeoutSeconds(profile.firstResponseTimeoutMs)),
        deadlineExceeded: semanticResultTimeoutMs <= 0
      }
    }
    if (streamLifetimeTimeoutMs !== undefined && streamLifetimeTimeoutMs <= rawTimeoutMs) {
      return {
        phase: 'active_stream',
        timeoutMs: streamLifetimeTimeoutMs,
        rawTimeoutMs,
        semanticResultTimeoutMs: status.semanticResultReceived || status.pendingProtocolEvent || status.parserSkipped ? undefined : semanticResultTimeoutMs,
        streamLifetimeTimeoutMs,
        timeoutKind: 'stream_lifetime',
        timeoutMessage: streamMaxLifetimeTimeoutMessage(timeoutSeconds(profile.uncommittedAttemptMaxLifetimeMs)),
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
      timeoutMessage: streamIdleTimeoutMessage(timeoutSeconds(profile.idleTimeoutMs)),
      deadlineExceeded: rawTimeoutMs <= 0
    }
  }

  const firstChunkTimeoutMs = profile.firstResponseTimeoutMs - (now - startedAt)
  if (streamLifetimeTimeoutMs !== undefined && streamLifetimeTimeoutMs <= firstChunkTimeoutMs) {
    return {
      phase: 'first_chunk',
      timeoutMs: streamLifetimeTimeoutMs,
      streamLifetimeTimeoutMs,
      timeoutKind: 'stream_lifetime',
      timeoutMessage: streamMaxLifetimeTimeoutMessage(timeoutSeconds(profile.uncommittedAttemptMaxLifetimeMs)),
      deadlineExceeded: streamLifetimeTimeoutMs <= 0
    }
  }
  return {
    phase: 'first_chunk',
    timeoutMs: firstChunkTimeoutMs,
    streamLifetimeTimeoutMs,
    timeoutKind: 'first_chunk',
    timeoutMessage: firstChunkTimeoutMessage(timeoutSeconds(profile.firstResponseTimeoutMs)),
    deadlineExceeded: firstChunkTimeoutMs <= 0
  }
}

function timeoutSeconds(timeoutMs: number): number {
  return Math.max(1, Math.ceil(timeoutMs / 1000))
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
