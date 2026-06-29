import {
  gatewayStreamClientRetryErrorCode,
  gatewayStreamClientRetryMessage
} from './responses.js'
import {
  type ResponseEndpointFamily,
  type ResponseSemanticFrame
} from '../protocols/openai-v1/response-semantics.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  responseInspectionPolicyActionRuntime,
  type ResponseInspectionPolicyAccountState,
  type ResponseInspectionPolicyAccountSwitch,
  type ResponseInspectionPolicyClientProfile,
  type ResponseInspectionPolicyDataHandling,
  type ResponseInspectionPolicyExecutionMode,
  type ResponseInspectionPolicyMatch,
  type ResponseInspectionPolicyScopeType,
  type ResponseInspectionPolicySource,
  type ResponseInspectionPolicySummary
} from '../../../storage/response-inspection-policy.repository.js'
import { normalizeAccountResponseInspectionRules } from '../../accounts/account-response-inspection-policy-validation.js'
import { OPENAI_PROTOCOL_CODE } from '../../../domain/provider-protocol.js'
import type { AccountClientCompatibility } from '../../../domain/types.js'

export interface RuntimeResponseInspectionPolicy {
  id: string
  source: ResponseInspectionPolicySource
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: string
  providerCode?: string
  match: ResponseInspectionPolicyMatch
  action: ResponseInspectionPolicySummary['action']
  executionMode: ResponseInspectionPolicyExecutionMode
  dataHandling: ResponseInspectionPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: ResponseInspectionPolicyAccountSwitch
  accountState: ResponseInspectionPolicyAccountState
}

export interface ResponseInspectionMatchResult {
  policy: RuntimeResponseInspectionPolicy
  matchedFrame: ResponseSemanticFrame
  matchedField: string
  matchedValue: string
  snippet?: string
}

export interface ResponseInspectionRuntimeContext {
  clientProfile: ResponseInspectionPolicyClientProfile
  accountClientCompatibility?: AccountClientCompatibility
  codexCompactionExpected?: boolean
}

export interface ResponseInspectionDecision {
  reason: 'configured_response_policy' | 'before_downstream_write_response_failure' | 'cyber_policy_response_failure'
  action: 'client_retry' | 'discard_event' | 'discard_response' | 'replace_with_failure' | 'dry_run'
  transport: 'json' | 'sse'
  triggerPhase: 'before_downstream_write' | 'after_downstream_write'
  endpointFamily: ResponseEndpointFamily
  frameType: ResponseSemanticFrame['frameType']
  upstreamEventType?: string
  upstreamErrorCode?: string
  upstreamErrorType?: string
  upstreamErrorMessage?: string
  finishReason?: string
  clientProfile?: ResponseInspectionPolicyClientProfile
  codexCompactionExpected?: boolean
  rewriteErrorCode?: string
  rewriteMessage?: string
  downstreamWritten: boolean
  policyId?: string
  policyName?: string
  policySource?: ResponseInspectionPolicySource
  policyScopeType?: ResponseInspectionPolicyScopeType
  policyProtocolCode?: string
  policyProviderCode?: string
  executionMode?: ResponseInspectionPolicyExecutionMode
  dataHandling?: ResponseInspectionPolicyDataHandling
  retryEnabled?: boolean
  accountSwitch?: string
  accountState?: string
  matchedField?: string
  matchedValue?: string
  matchedSnippet?: string
}

export interface ResponseInspectionResult {
  decision?: ResponseInspectionDecision
  observations?: ResponseInspectionDecision[]
}

export interface ResponseInspectionFailurePayload {
  errorCode: string
  message: string
}

const textMatchSnippetChars = 160

export function resolveRuntimeResponseInspectionPolicies(input: {
  account: UpstreamAccount
  managementPolicies?: ResponseInspectionPolicySummary[]
}): RuntimeResponseInspectionPolicy[] {
  const management = (input.managementPolicies ?? [])
    .filter((policy) => policyMatchesAccountScope(policy, input.account))
    .map((policy) => runtimePolicyFromSummary(policy, policy.defaultRule ? 'system_default' : 'management'))
  const accountRules = accountResponseInspectionRules(input.account)
  return [...accountRules, ...management].sort((a, b) =>
    sourceOrder(a.source) - sourceOrder(b.source)
    || scopeOrder(a) - scopeOrder(b)
    || a.priority - b.priority
    || a.id.localeCompare(b.id)
  )
}

export function inspectResponseSemanticFrames(input: {
  frames: ResponseSemanticFrame[]
  policies: RuntimeResponseInspectionPolicy[]
  downstreamWritten: boolean
  transport: 'json' | 'sse'
  context?: ResponseInspectionRuntimeContext
}): ResponseInspectionResult {
  const observations: ResponseInspectionDecision[] = []
  for (const frame of input.frames) {
    const match = matchRuntimeResponseInspectionPolicy(frame, input.policies, input.context)
    if (!match) continue
    const policy = match.policy
    const action = responseInspectionDecisionAction(policy, input.transport)
    const decision = buildPolicyDecision(match, input.downstreamWritten, action, input.context)
    if (policy.executionMode === 'dry_run') {
      observations.push(decision)
      continue
    }
    if (action === 'discard_event') {
      return {
        decision,
        observations: [...observations, decision]
      }
    }
    return {
      decision,
      observations: observations.length > 0 ? observations : undefined
    }
  }
  return observations.length > 0 ? { observations } : {}
}

export function responseInspectionFailurePayloadForDecision(
  decision: ResponseInspectionDecision,
  clientRetryEnabled: boolean
): ResponseInspectionFailurePayload {
  const clientRetryCode = clientRetryEnabled
    && decision.retryEnabled === true
    && decision.triggerPhase === 'before_downstream_write'
    ? gatewayStreamClientRetryErrorCode
    : undefined
  const errorCode = clientRetryCode ?? decision.rewriteErrorCode ?? 'response_inspection_matched'
  const message = clientRetryCode === gatewayStreamClientRetryErrorCode
    ? gatewayStreamClientRetryMessage
    : decision.rewriteMessage ?? '响应命中检查策略'
  return { errorCode, message }
}

export function isCodexRetryableAfterOutputResponseFailureCode(errorCode: string | undefined): boolean {
  return errorCode === 'cyber_policy'
}

export function matchRuntimeResponseInspectionPolicy(
  frame: ResponseSemanticFrame,
  policies: RuntimeResponseInspectionPolicy[],
  context?: ResponseInspectionRuntimeContext
): ResponseInspectionMatchResult | undefined {
  for (const policy of policies) {
    if (!policy.enabled) continue
    if (!policyMatchesRuntimeContext(policy.match, context)) continue
    const match = firstPositiveMatch(frame, policy.match)
    if (!match) continue
    if (outputTextExcluded(frame, policy.match)) continue
    return {
      policy,
      matchedFrame: frame,
      ...match
    }
  }
  return undefined
}

function firstPositiveMatch(frame: ResponseSemanticFrame, match: ResponseInspectionPolicyMatch): Pick<ResponseInspectionMatchResult, 'matchedField' | 'matchedValue' | 'snippet'> | undefined {
  const matched: Array<Pick<ResponseInspectionMatchResult, 'matchedField' | 'matchedValue' | 'snippet'>> = []

  if (match.outputTextIncludes?.length) {
    if (!frame.text || frame.visibleOutput === false) return undefined
    const outputTextMatch = firstSubstringMatch(frame.text, match.outputTextIncludes)
    if (!outputTextMatch) return undefined
    matched.push({ matchedField: 'outputTextIncludes', matchedValue: outputTextMatch, snippet: snippetAround(frame.text, outputTextMatch) })
  }

  if (match.errorCodes?.length) {
    const errorCode = firstExactMatch(frame.errorCode, match.errorCodes)
    if (!errorCode) return undefined
    matched.push({ matchedField: 'errorCodes', matchedValue: errorCode, snippet: frame.errorCode })
  }

  if (match.errorTypes?.length) {
    const errorType = firstExactMatch(frame.errorType, match.errorTypes)
    if (!errorType) return undefined
    matched.push({ matchedField: 'errorTypes', matchedValue: errorType, snippet: frame.errorType })
  }

  if (match.errorMessageIncludes?.length) {
    if (!frame.errorMessage) return undefined
    const errorMessageMatch = firstSubstringMatch(frame.errorMessage, match.errorMessageIncludes)
    if (!errorMessageMatch) return undefined
    matched.push({ matchedField: 'errorMessageIncludes', matchedValue: errorMessageMatch, snippet: snippetAround(frame.errorMessage, errorMessageMatch) })
  }

  if (match.finishReasons?.length) {
    const finishReason = firstExactMatch(frame.finishReason ?? frame.status, match.finishReasons)
    if (!finishReason) return undefined
    matched.push({ matchedField: 'finishReasons', matchedValue: finishReason, snippet: frame.finishReason ?? frame.status })
  }

  if (match.jsonPathsExists?.length) {
    const jsonPath = firstJsonPathMatch(frame, match.jsonPathsExists)
    if (!jsonPath) return undefined
    matched.push({ matchedField: 'jsonPathsExists', matchedValue: jsonPath, snippet: jsonPath })
  }

  if (match.rawTextIncludes?.length) {
    if (!frame.rawText) return undefined
    const rawTextMatch = firstSubstringMatch(frame.rawText, match.rawTextIncludes)
    if (!rawTextMatch) return undefined
    matched.push({ matchedField: 'rawTextIncludes', matchedValue: rawTextMatch, snippet: snippetAround(frame.rawText, rawTextMatch) })
  }

  return matched.find((item) => item.snippet) ?? matched[0]
}

function outputTextExcluded(frame: ResponseSemanticFrame, match: ResponseInspectionPolicyMatch): boolean {
  if (!frame.text || frame.visibleOutput === false || !match.outputTextIncludes?.length || !match.outputTextExcludes?.length) return false
  return Boolean(firstSubstringMatch(frame.text, match.outputTextExcludes))
}

function buildPolicyDecision(
  match: ResponseInspectionMatchResult,
  downstreamWritten: boolean,
  action: ResponseInspectionDecision['action'],
  context?: ResponseInspectionRuntimeContext
): ResponseInspectionDecision {
  const policy = match.policy
  const frame = match.matchedFrame
  return {
    reason: configuredPolicyReason(policy, frame, downstreamWritten),
    action,
    transport: frame.transport,
    triggerPhase: downstreamWritten ? 'after_downstream_write' : 'before_downstream_write',
    endpointFamily: frame.endpointFamily,
    frameType: frame.frameType,
    upstreamEventType: frame.eventType,
    upstreamErrorCode: frame.errorCode,
    upstreamErrorType: frame.errorType,
    upstreamErrorMessage: frame.errorMessage,
    finishReason: frame.finishReason ?? frame.status,
    clientProfile: context?.clientProfile,
    codexCompactionExpected: context?.codexCompactionExpected,
    rewriteErrorCode: rewriteErrorCode(policy, frame),
    rewriteMessage: frame.errorMessage ?? `响应命中检查策略：${policy.name}`,
    downstreamWritten,
    policyId: policy.id,
    policyName: policy.name,
    policySource: policy.source,
    policyScopeType: policy.scopeType,
    policyProtocolCode: policy.protocolCode,
    policyProviderCode: policy.providerCode,
    executionMode: policy.executionMode,
    dataHandling: policy.dataHandling,
    retryEnabled: policy.retryEnabled,
    accountSwitch: policy.accountSwitch,
    accountState: policy.accountState,
    matchedField: match.matchedField,
    matchedValue: match.matchedValue,
    matchedSnippet: match.snippet
  }
}

function policyMatchesRuntimeContext(
  match: ResponseInspectionPolicyMatch,
  context: ResponseInspectionRuntimeContext | undefined
): boolean {
  if (usesUpstreamErrorIdentityMatcher(match) && !match.clientProfiles?.length) {
    return false
  }
  if (match.clientProfiles?.length) {
    if (!context?.clientProfile || !firstExactMatch(context.clientProfile, match.clientProfiles)) return false
  }
  return true
}

function usesUpstreamErrorIdentityMatcher(match: ResponseInspectionPolicyMatch): boolean {
  return Boolean(match.errorCodes?.length || match.errorTypes?.length)
}

function configuredPolicyReason(policy: RuntimeResponseInspectionPolicy, frame: ResponseSemanticFrame, downstreamWritten: boolean): ResponseInspectionDecision['reason'] {
  if (policy.source !== 'system_default') return 'configured_response_policy'
  return downstreamWritten && frame.errorCode === 'cyber_policy'
    ? 'cyber_policy_response_failure'
    : 'before_downstream_write_response_failure'
}

function responseInspectionDecisionAction(
  policy: RuntimeResponseInspectionPolicy,
  transport: 'json' | 'sse'
): ResponseInspectionDecision['action'] {
  if (policy.executionMode === 'dry_run') return 'dry_run'
  if (policy.dataHandling === 'discard_event' && transport === 'sse') return 'discard_event'
  if (policy.dataHandling === 'discard_event') return 'dry_run'
  if (policy.dataHandling === 'discard_response') return 'discard_response'
  return 'replace_with_failure'
}

function rewriteErrorCode(policy: RuntimeResponseInspectionPolicy, frame: ResponseSemanticFrame): string {
  if (policy.retryEnabled && frame.transport === 'sse' && frame.errorCode === 'cyber_policy') {
    return gatewayStreamClientRetryErrorCode
  }
  return frame.errorCode ?? 'response_inspection_matched'
}

function policyMatchesAccountScope(policy: ResponseInspectionPolicySummary, account: UpstreamAccount): boolean {
  if (!policy.enabled) return false
  if (policy.scopeType === 'protocol') return policy.protocolCode === account.protocolCode && policy.providerCode === undefined
  return policy.protocolCode === account.protocolCode && policy.providerCode === account.providerCode
}

function runtimePolicyFromSummary(policy: ResponseInspectionPolicySummary, source: ResponseInspectionPolicySource): RuntimeResponseInspectionPolicy {
  const runtime = responseInspectionPolicyActionRuntime(policy.action)
  return {
    id: policy.id,
    source,
    name: policy.name,
    enabled: policy.enabled,
    priority: policy.priority,
    scopeType: policy.scopeType,
    protocolCode: policy.protocolCode,
    providerCode: policy.providerCode,
    match: policy.match,
    action: policy.action,
    ...runtime
  }
}

function sourceOrder(source: ResponseInspectionPolicySource): number {
  if (source === 'account') return 0
  if (source === 'management') return 1
  return 2
}

function scopeOrder(policy: RuntimeResponseInspectionPolicy): number {
  if (policy.source === 'account') return 0
  return policy.scopeType === 'provider' ? 0 : 1
}

function firstExactMatch(value: string | undefined, needles: string[] | undefined): string | undefined {
  if (!value || !needles?.length) return undefined
  const normalized = value.toLowerCase()
  return needles.find((needle) => needle.toLowerCase() === normalized)
}

function firstSubstringMatch(value: string, needles: string[] | undefined): string | undefined {
  if (!needles?.length) return undefined
  const normalized = value.toLowerCase()
  return needles.find((needle) => normalized.includes(needle.toLowerCase()))
}

function firstJsonPathMatch(frame: ResponseSemanticFrame, needles: string[] | undefined): string | undefined {
  if (!needles?.length) return undefined
  return needles.find((needle) =>
    (frame.rawJson !== undefined && jsonPathExists(frame.rawJson, needle))
    || Boolean(frame.rawJsonPaths?.includes(needle))
  )
}

function jsonPathExists(value: unknown, path: string): boolean {
  const parts = path.split('.').map((part) => part.trim()).filter(Boolean)
  if (!parts.length) return false
  let current: unknown = value
  for (const part of parts) {
    if (Array.isArray(current)) {
      const index = Number(part)
      if (!Number.isInteger(index) || index < 0 || index >= current.length) return false
      current = current[index]
      continue
    }
    if (typeof current !== 'object' || current === null) return false
    if (!Object.prototype.hasOwnProperty.call(current, part)) return false
    current = (current as Record<string, unknown>)[part]
  }
  return hasJsonPathMeaningfulValue(current)
}

function hasJsonPathMeaningfulValue(value: unknown): boolean {
  if (value === null || value === undefined || value === false) return false
  if (typeof value === 'string') return value.trim().length > 0
  if (Array.isArray(value)) return value.length > 0
  if (typeof value === 'object') return hasOwnEnumerableKey(value as Record<string, unknown>)
  return true
}

function hasOwnEnumerableKey(value: Record<string, unknown>): boolean {
  for (const key in value) {
    if (Object.prototype.hasOwnProperty.call(value, key)) return true
  }
  return false
}

function accountResponseInspectionRules(account: UpstreamAccount): RuntimeResponseInspectionPolicy[] {
  const rules = normalizeAccountResponseInspectionRules(account.credentials.response_inspection_rules)
  return rules.map((rule, index) => {
    const runtime = responseInspectionPolicyActionRuntime(rule.action)
    return {
      id: `account_rule_${index + 1}`,
      source: 'account',
      name: rule.name,
      enabled: rule.enabled,
      priority: rule.priority,
      scopeType: 'provider',
      protocolCode: account.protocolCode || OPENAI_PROTOCOL_CODE,
      providerCode: account.providerCode,
      match: rule.match,
      action: rule.action,
      ...runtime
    }
  })
}

function snippetAround(value: string, needle: string): string {
  const index = value.toLowerCase().indexOf(needle.toLowerCase())
  if (index < 0) return value.slice(0, textMatchSnippetChars)
  const start = Math.max(0, index - 40)
  const end = Math.min(value.length, index + needle.length + 80)
  return value.slice(start, end)
}
