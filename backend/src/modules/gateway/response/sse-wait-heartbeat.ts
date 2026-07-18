import type { Response } from 'express'

import type { OpenAIGatewayDownstreamProtocol } from '../client-profiles/strategy.js'
import type { ServerRetryBudgetWaitObserver } from '../runtime/server-retry-budget.js'
import { GatewayDownstreamCommitState } from './downstream-commit-state.js'

const gatewaySseWaitHeartbeatIntervalMs = 15_000
const gatewaySseWaitHeartbeatChunk = Buffer.from(': juhe-ai waiting for upstream capacity\n\n')

export function createGatewaySseWaitHeartbeatObserver(input: {
  res: Response
  downstreamProtocol: OpenAIGatewayDownstreamProtocol
  downstreamCommitState: GatewayDownstreamCommitState
  signal?: AbortSignal
  intervalMs?: number
}): ServerRetryBudgetWaitObserver | undefined {
  if (!gatewayDownstreamProtocolUsesSse(input.downstreamProtocol)) return undefined
  let timer: NodeJS.Timeout | undefined
  let abortListener: (() => void) | undefined
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
  const writeHeartbeat = () => {
    if (input.signal?.aborted || input.res.writableEnded || input.res.destroyed || input.downstreamCommitState.semanticCommitted) {
      stop()
      return
    }
    if (!input.res.headersSent) {
      input.res.status(200)
      input.res.setHeader('content-type', 'text/event-stream; charset=utf-8')
      input.res.setHeader('cache-control', 'no-cache, no-transform')
      input.res.setHeader('x-accel-buffering', 'no')
    }
    input.res.write(gatewaySseWaitHeartbeatChunk)
    input.downstreamCommitState.markTransportCommitted(gatewaySseWaitHeartbeatChunk.length)
  }
  return {
    onWaitStarted: () => {
      if (timer || input.signal?.aborted || input.res.writableEnded || input.res.destroyed || input.downstreamCommitState.semanticCommitted) return
      writeHeartbeat()
      if (input.res.writableEnded || input.res.destroyed || input.downstreamCommitState.semanticCommitted) return
      timer = setInterval(writeHeartbeat, Math.max(1000, input.intervalMs ?? gatewaySseWaitHeartbeatIntervalMs))
      timer.unref?.()
      if (input.signal) {
        abortListener = stop
        input.signal.addEventListener('abort', abortListener, { once: true })
      }
    },
    onWaitPaused: stop
  }
}

export function gatewayDownstreamProtocolUsesSse(protocol: OpenAIGatewayDownstreamProtocol): boolean {
  return protocol === 'responses_sse'
    || protocol === 'chat_completions_sse'
    || protocol === 'messages_sse'
    || protocol === 'gemini_stream_generate_content_sse'
    || protocol === 'unknown_stream'
}
