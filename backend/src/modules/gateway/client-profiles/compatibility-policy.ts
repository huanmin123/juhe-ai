import type { Request } from 'express'

import { sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  recoverOpenAIResponsesEncryptedReasoningBody,
  type OpenAIResponsesEncryptedReasoningRecoveryResult
} from '../request/recovery.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'

export type GatewayCompatibilityErrorSignal = 'encrypted_reasoning_invalid'

export type GatewayCompatibilityRecoveryDecision =
  | { action: 'continue_default'; signal?: GatewayCompatibilityErrorSignal }
  | {
      action: 'retry_with_body_variant'
      signal: GatewayCompatibilityErrorSignal
      body: Buffer
      recoveryKey: string
      metadata: GatewayCompatibilityRecoveryMetadata
    }

export interface GatewayCompatibilityRecoveryMetadata extends Record<string, unknown> {
  strategy: 'responses_encrypted_reasoning_cleanup'
  accountId: string
  upstreamUrl: string
  signal: GatewayCompatibilityErrorSignal
  hasEncryptedReasoning?: boolean
  encryptedReasoningItemCount?: number
  hasPreviousResponseId?: boolean
  hasFunctionCallOutput?: boolean
  removedEncryptedReasoningItemCount?: number
  removedReasoningItemCount?: number
  removedPreviousResponseId?: boolean
  bodyBytesBefore?: number
  bodyBytesAfter?: number
}

export interface GatewayCompatibilityRecoveryState {
  attemptedRecoveryKeys: Set<string>
}

export function createGatewayCompatibilityRecoveryState(): GatewayCompatibilityRecoveryState {
  return {
    attemptedRecoveryKeys: new Set()
  }
}

export async function decideGatewayCompatibilityRecovery(input: {
  req: Request
  account: UpstreamAccount
  upstreamUrl: string
  body: Buffer | string | undefined
  responseBodyText: string
  parsedError: Record<string, unknown>
  recoveryState: GatewayCompatibilityRecoveryState
  signal?: AbortSignal
}): Promise<GatewayCompatibilityRecoveryDecision> {
  const signal = classifyGatewayCompatibilityError({
    parsedError: input.parsedError,
    responseBodyText: input.responseBodyText
  })
  if (!signal) {
    return { action: 'continue_default' }
  }

  const recoveryKey = gatewayCompatibilityRecoveryKey(input.account, input.upstreamUrl, signal)
  if (input.recoveryState.attemptedRecoveryKeys.has(recoveryKey)) {
    return {
      action: 'continue_default',
      signal
    }
  }

  if (signal === 'encrypted_reasoning_invalid') {
    const recovery = await recoverOpenAIResponsesEncryptedReasoningBody(input.req, input.body, input.signal)
    if (!recovery) {
      return { action: 'continue_default', signal }
    }

    input.recoveryState.attemptedRecoveryKeys.add(recoveryKey)
    return {
      action: 'retry_with_body_variant',
      signal,
      body: recovery.body,
      recoveryKey,
      metadata: gatewayCompatibilityRecoveryMetadata(input, signal, {
        recovery
      })
    }
  }

  return { action: 'continue_default', signal }
}

export function recordGatewayCompatibilityRecoveryDecision(
  auditCapture: AuditCaptureContext,
  decision: GatewayCompatibilityRecoveryDecision
): void {
  if (decision.action === 'continue_default') {
    return
  }
  auditCapture.addGatewayMetadata({
    label: 'compatibility_recovery_retry',
    metadata: decision.metadata
  })
}

export function classifyGatewayCompatibilityError(input: {
  parsedError: Record<string, unknown>
  responseBodyText: string
}): GatewayCompatibilityErrorSignal | undefined {
  const code = stringValue(input.parsedError.code).toLowerCase()
  const message = stringValue(input.parsedError.message).toLowerCase()
  const bodyText = input.responseBodyText.toLowerCase()
  const combined = `${code}\n${message}\n${bodyText}`

  if (combined.includes('thinking_signature_invalid') || combined.includes('invalid_encrypted_content')) {
    return 'encrypted_reasoning_invalid'
  }
  if (
    combined.includes('encrypted content')
    && (
      combined.includes('could not be decrypted')
      || combined.includes('could not be verified')
      || combined.includes('could not be decrypted or parsed')
    )
  ) {
    return 'encrypted_reasoning_invalid'
  }
  return undefined
}

function gatewayCompatibilityRecoveryKey(
  account: UpstreamAccount,
  upstreamUrl: string,
  signal: GatewayCompatibilityErrorSignal
): string {
  return [
    account.id,
    sanitizeUrlCredentialsForLog(upstreamUrl) ?? upstreamUrl,
    signal
  ].join('|')
}

function gatewayCompatibilityRecoveryMetadata(
  input: {
    account: UpstreamAccount
    upstreamUrl: string
    body: Buffer | string | undefined
  },
  signal: GatewayCompatibilityErrorSignal,
  options: {
    recovery?: OpenAIResponsesEncryptedReasoningRecoveryResult
  } = {}
): GatewayCompatibilityRecoveryMetadata {
  const bodyBytesBefore = typeof input.body === 'string'
    ? Buffer.byteLength(input.body, 'utf8')
    : input.body?.byteLength
  const recovery = options.recovery
  return {
    strategy: 'responses_encrypted_reasoning_cleanup',
    accountId: input.account.id,
    upstreamUrl: sanitizeUrlCredentialsForLog(input.upstreamUrl) ?? input.upstreamUrl,
    signal,
    hasEncryptedReasoning: recovery?.features.hasEncryptedReasoning,
    encryptedReasoningItemCount: recovery?.features.encryptedReasoningItemCount,
    hasPreviousResponseId: recovery?.features.hasPreviousResponseId,
    hasFunctionCallOutput: recovery?.features.hasFunctionCallOutput,
    removedEncryptedReasoningItemCount: recovery?.removedEncryptedReasoningItemCount,
    removedReasoningItemCount: recovery?.removedReasoningItemCount,
    removedPreviousResponseId: recovery?.removedPreviousResponseId,
    bodyBytesBefore,
    bodyBytesAfter: recovery?.body.byteLength
  }
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}
