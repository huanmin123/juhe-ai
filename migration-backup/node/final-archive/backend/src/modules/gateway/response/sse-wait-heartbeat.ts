import type { Response } from 'express'

import type { OpenAIGatewayDownstreamProtocol } from '../client-profiles/strategy.js'
import type { ServerRetryBudgetWaitObserver } from '../runtime/server-retry-budget.js'
import { GatewayDownstreamCommitState } from './downstream-commit-state.js'

const gatewaySseWaitHeartbeatIntervalMs = 15_000
const gatewaySseWaitHeartbeatChunk = Buffer.from(': juhe-ai waiting for upstream capacity\n\n')
const codexCompactionSseWaitHeartbeatChunk = Buffer.from('data: {"type":"juhe_ai.keepalive"}\n\n', 'utf8')

export interface GatewaySseWaitHeartbeat {
  start: () => void
  stop: () => void
}

export function createGatewaySseWaitHeartbeatObserver(input: {
  res: Response
  downstreamProtocol: OpenAIGatewayDownstreamProtocol
  downstreamCommitState: GatewayDownstreamCommitState
  signal?: AbortSignal
  intervalMs?: number
}): ServerRetryBudgetWaitObserver | undefined {
  const heartbeat = createGatewaySseWaitHeartbeat(input)
  if (!heartbeat) return undefined
  return {
    onWaitStarted: heartbeat.start,
    onWaitPaused: heartbeat.stop
  }
}

export function createGatewaySseWaitHeartbeat(input: {
  res: Response
  downstreamProtocol: OpenAIGatewayDownstreamProtocol
  downstreamCommitState: GatewayDownstreamCommitState
  signal?: AbortSignal
  intervalMs?: number
  emitCodexCompactionKeepalive?: boolean
}): GatewaySseWaitHeartbeat | undefined {
  if (!gatewayDownstreamProtocolUsesSse(input.downstreamProtocol)) return undefined
  const heartbeatChunk = input.emitCodexCompactionKeepalive === true
    && input.downstreamProtocol === 'responses_sse'
    ? codexCompactionSseWaitHeartbeatChunk
    : gatewaySseWaitHeartbeatChunk
  let timer: NodeJS.Timeout | undefined
  let abortListener: (() => void) | undefined
  let responseListenersAttached = false
  const stop = () => {
    if (timer) {
      clearInterval(timer)
      timer = undefined
    }
    if (input.signal && abortListener) {
      input.signal.removeEventListener('abort', abortListener)
      abortListener = undefined
    }
  }
  const detachResponseListeners = () => {
    if (!responseListenersAttached) return
    input.res.off('close', handleResponseClosed)
    input.res.off('error', handleResponseError)
    responseListenersAttached = false
  }
  const handleResponseClosed = () => {
    stop()
    detachResponseListeners()
  }
  const handleResponseError = () => {
    stop()
    detachResponseListeners()
  }
  const stopAndDetach = () => {
    stop()
    detachResponseListeners()
  }
  const attachResponseListeners = () => {
    if (responseListenersAttached) return
    responseListenersAttached = true
    input.res.once('close', handleResponseClosed)
    input.res.once('error', handleResponseError)
  }
  const writeHeartbeat = (): boolean => {
    if (input.signal?.aborted || input.res.writableEnded || input.res.destroyed || input.downstreamCommitState.semanticCommitted) {
      stopAndDetach()
      return false
    }
    try {
      if (!input.res.headersSent) {
        input.res.status(200)
        input.res.setHeader('content-type', 'text/event-stream; charset=utf-8')
        input.res.setHeader('cache-control', 'no-cache, no-transform')
        input.res.setHeader('x-accel-buffering', 'no')
      }
      input.res.write(heartbeatChunk)
      input.downstreamCommitState.markTransportCommitted(heartbeatChunk.length)
      return true
    } catch {
      stopAndDetach()
      return false
    }
  }
  return {
    start: () => {
      if (timer || input.signal?.aborted || input.res.writableEnded || input.res.destroyed || input.downstreamCommitState.semanticCommitted) return
      attachResponseListeners()
      if (!writeHeartbeat()) return
      timer = setInterval(writeHeartbeat, Math.max(1000, input.intervalMs ?? gatewaySseWaitHeartbeatIntervalMs))
      timer.unref?.()
      if (input.signal) {
        abortListener = stopAndDetach
        input.signal.addEventListener('abort', abortListener, { once: true })
      }
    },
    stop: stopAndDetach
  }
}

export function gatewayDownstreamProtocolUsesSse(protocol: OpenAIGatewayDownstreamProtocol): boolean {
  return protocol === 'responses_sse'
    || protocol === 'chat_completions_sse'
    || protocol === 'messages_sse'
    || protocol === 'gemini_stream_generate_content_sse'
    || protocol === 'unknown_stream'
}
