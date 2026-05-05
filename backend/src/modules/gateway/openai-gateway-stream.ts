import type { Response } from 'express'

import type { GatewaySettings } from './account-error-policy.service.js'
import { inspectOpenAIStreamText } from './openai-gateway-usage.js'
import { readStreamChunkWithIdleTimeout } from './openai-gateway-upstream.js'
import { writeGatewayStreamFailureEvent } from './openai-gateway-responses.js'

export interface StreamPipeResult {
  completed: boolean
  chunks: Buffer[]
  upstreamChunks: Buffer[]
  message: string
  firstTokenMs?: number
}

export async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  settings: GatewaySettings,
  startedAt: number,
  handleStreamFailure: (reason: string) => void
): Promise<StreamPipeResult> {
  const chunks: Buffer[] = []
  const upstreamChunks: Buffer[] = []
  const iterator = upstreamBody[Symbol.asyncIterator]()
  let completed = false
  let firstTokenMs: number | undefined

  try {
    while (true) {
      const result = settings.streamCircuitBreakerEnabled
        ? await readStreamChunkWithIdleTimeout(iterator, settings.streamIdleTimeoutSeconds)
        : await iterator.next()

      if (result.done) {
        completed = true
        break
      }

      const buffer = Buffer.from(result.value)
      chunks.push(buffer)
      upstreamChunks.push(buffer)
      if (firstTokenMs === undefined && inspectOpenAIStreamText(Buffer.concat(chunks).toString('utf8')).outputReceived) {
        firstTokenMs = Date.now() - startedAt
      }
      res.write(buffer)
    }
  } catch (error) {
    const rawMessage = error instanceof Error ? error.message : 'Upstream stream interrupted'
    const inspection = inspectOpenAIStreamText(Buffer.concat(chunks).toString('utf8'))
    if (inspection.terminalReceived && !inspection.failedReceived) {
      res.end()
      return { completed: true, chunks, upstreamChunks, message: 'completed', firstTokenMs }
    }
    const message = inspection.errorMessage ?? rawMessage
    handleStreamFailure(message)
    if (!inspection.failedReceived) {
      const failureEvent = writeGatewayStreamFailureEvent(res, message)
      if (failureEvent) {
        chunks.push(failureEvent)
      }
    }
    res.end()
    return { completed: false, chunks, upstreamChunks, message, firstTokenMs }
  }

  const inspection = inspectOpenAIStreamText(Buffer.concat(chunks).toString('utf8'))
  if (!inspection.terminalReceived) {
    const message = 'Upstream stream ended before OpenAI terminal event'
    handleStreamFailure(message)
    const failureEvent = writeGatewayStreamFailureEvent(res, message)
    if (failureEvent) {
      chunks.push(failureEvent)
    }
    res.end()
    return { completed: false, chunks, upstreamChunks, message, firstTokenMs }
  }

  res.end()

  if (!completed || inspection.failedReceived) {
    const message = inspection.errorMessage ?? 'Upstream stream failed'
    handleStreamFailure(message)
    return { completed: false, chunks, upstreamChunks, message, firstTokenMs }
  }

  return { completed: true, chunks, upstreamChunks, message: 'completed', firstTokenMs }
}
