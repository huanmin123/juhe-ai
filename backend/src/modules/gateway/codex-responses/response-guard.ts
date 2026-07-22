import { codexResponsesContractRevision } from './contract-registry.js'
import { validateCodexResponsesJson } from './contract-validator.js'
import { executeCodexResponsesRepair } from './repair-executor.js'
import {
  planCodexResponsesJsonRepair,
  type CodexItemIdFactoryInput
} from './repair-planner.js'
import {
  createCodexResponsesStreamContractState,
  type CodexStreamContractInput,
  type CodexStreamIdRepair,
  type CodexStreamContractSnapshot,
  type CodexStreamContractEventResult
} from './stream-contract-state.js'
import type {
  CodexContractOutcome,
  CodexContractValidationResult,
  CodexProtocolIssue
} from './contract-types.js'
import { GatewayDownstreamCommitState } from '../response/downstream-commit-state.js'
import type { ParsedOpenAIStreamEvent } from '../protocols/openai-v1/stream-events.js'

type JsonRecord = Record<string, unknown>

const codexResponsesGuardMarkerKind = 'codex_responses_guard_checkpoint_v1'

export const codexResponsesGuardDiagnosticLimit = 32

export type CodexResponsesGuardCheckpoint = 'raw_upstream' | 'gateway_bridge'
export type CodexResponsesGuardMode = 'shadow' | 'safe_repair'
export type CodexResponsesGuardOutcome =
  | CodexContractOutcome
  | 'repaired_safe'
  | 'repaired_bridge'

export interface CodexResponsesGuardMarker {
  kind: typeof codexResponsesGuardMarkerKind
  checkpoint: CodexResponsesGuardCheckpoint
}

export interface CodexResponsesGuardCommitSnapshot {
  transportCommitted: boolean
  semanticCommitted: boolean
  downstreamBytesWritten: number
}

export interface CodexResponsesGuardResultBase {
  revision: typeof codexResponsesContractRevision
  checkpoint: CodexResponsesGuardCheckpoint
  provenance: CodexResponsesGuardCheckpoint
  mode: CodexResponsesGuardMode
  outcome: CodexResponsesGuardOutcome
  issues: readonly CodexProtocolIssue[]
  omittedIssueCount: number
  retryable: boolean
  repairRuleIds: readonly string[]
  commit: CodexResponsesGuardCommitSnapshot
}

export interface CodexResponsesGuardJsonResult extends CodexResponsesGuardResultBase {
  value: JsonRecord
}

export interface CodexResponsesGuardSseResult extends CodexResponsesGuardResultBase {
  eventCategory: CodexStreamContractEventResult['eventCategory']
  repairs: readonly CodexStreamIdRepair[]
}

export interface CodexResponsesGuardSnapshot {
  revision: typeof codexResponsesContractRevision
  checkpoint: CodexResponsesGuardCheckpoint
  provenance: CodexResponsesGuardCheckpoint
  mode: CodexResponsesGuardMode
  outcome: CodexResponsesGuardOutcome
  retryable: boolean
  repairRuleIds: readonly string[]
  diagnostics: readonly CodexProtocolIssue[]
  omittedDiagnosticCount: number
  stream: CodexStreamContractSnapshot
  commit: CodexResponsesGuardCommitSnapshot
}

export interface CreateCodexResponsesResponseGuardInput {
  marker: CodexResponsesGuardMarker
  downstreamCommitState: GatewayDownstreamCommitState
  mode?: CodexResponsesGuardMode
  createItemId?: (input: CodexItemIdFactoryInput) => string
  envelopeKind?: 'response' | 'compact'
}

/**
 * Builds an explicit checkpoint marker for the response boundary that actually
 * produced the inspected bytes. Callers must carry this marker; the guard never
 * guesses provenance from response shape, account type, or endpoint metadata.
 */
export function createCodexResponsesGuardMarker(
  checkpoint: CodexResponsesGuardCheckpoint
): Readonly<CodexResponsesGuardMarker> {
  return Object.freeze({ kind: codexResponsesGuardMarkerKind, checkpoint })
}

export function isCodexResponsesGuardMarker(value: unknown): value is CodexResponsesGuardMarker {
  const marker = plainObject(value)
  return marker?.kind === codexResponsesGuardMarkerKind
    && (marker.checkpoint === 'raw_upstream' || marker.checkpoint === 'gateway_bridge')
}

export class CodexResponsesResponseGuard {
  readonly #marker: Readonly<CodexResponsesGuardMarker>
  readonly #downstreamCommitState: GatewayDownstreamCommitState
  readonly #mode: CodexResponsesGuardMode
  readonly #createItemId?: (input: CodexItemIdFactoryInput) => string
  readonly #envelopeKind: 'response' | 'compact'
  #streamState: ReturnType<typeof createCodexResponsesStreamContractState>
  readonly #diagnostics: CodexProtocolIssue[] = []
  #omittedDiagnosticCount = 0
  #aggregateOutcome: CodexResponsesGuardOutcome = 'clean'
  #responseResourceId: string | undefined
  readonly #appliedRepairRuleIds = new Set<string>()

  get mode(): CodexResponsesGuardMode {
    return this.#mode
  }

  constructor(input: CreateCodexResponsesResponseGuardInput) {
    if (!isCodexResponsesGuardMarker(input.marker)) {
      throw new TypeError('codex_responses_guard_marker_invalid')
    }
    this.#marker = createCodexResponsesGuardMarker(input.marker.checkpoint)
    this.#downstreamCommitState = input.downstreamCommitState
    this.#mode = input.mode ?? 'shadow'
    this.#createItemId = input.createItemId
    this.#envelopeKind = input.envelopeKind ?? 'response'
    this.#streamState = this.#createStreamState()
  }

  inspectJson(response: JsonRecord): CodexResponsesGuardJsonResult {
    const validation = validateResponseEnvelope(response, this.#marker.checkpoint, this.#envelopeKind)
      ?? validateCodexResponsesJson({
        response,
        provenance: this.#marker.checkpoint,
        revision: codexResponsesContractRevision
      })
    this.#recordDiagnostics(validation.issues)

    const bounded = boundedIssues(validation.issues)
    const committedOutcome = outcomeAtCommitBoundary(validation.outcome, this.#downstreamCommitState)
    if (committedOutcome === 'late_violation') {
      return this.#jsonResult(response, committedOutcome, bounded, false, [])
    }

    if (this.#mode === 'safe_repair' && validation.outcome === 'repairable') {
      const plan = planCodexResponsesJsonRepair({
        document: response,
        validation,
        downstreamExposed: this.#downstreamCommitState.semanticCommitted,
        createItemId: this.#createItemId
      })
      if (plan.level === 'R0' && plan.operations.length > 0) {
        try {
          const repaired = executeCodexResponsesRepair(response, plan)
          const repairRuleIds = unique(plan.operations.map((operation) => operation.ruleId))
          const outcome = this.#marker.checkpoint === 'gateway_bridge' ? 'repaired_bridge' : 'repaired_safe'
          return this.#jsonResult(repaired, outcome, bounded, false, repairRuleIds)
        } catch {
          const repairFailure = diagnosticIssue(
            'safe_repair_failed',
            this.#marker.checkpoint,
            'R2'
          )
          this.#recordDiagnostics([repairFailure])
          const failureIssues = boundedIssues([...validation.issues, repairFailure])
          return this.#jsonResult(
            response,
            'blocked',
            failureIssues,
            this.#downstreamCommitState.canRetryUpstream(),
            []
          )
        }
      }
    }

    const retryable = validation.outcome === 'blocked'
      && this.#downstreamCommitState.canRetryUpstream()
    return this.#jsonResult(response, committedOutcome, bounded, retryable, [])
  }

  inspectParsedSse(input: CodexStreamContractInput): CodexResponsesGuardSseResult {
    const result = this.#streamState.consume(input, {
      allowNewRepair: !this.#downstreamCommitState.semanticCommitted
    })
    this.#recordDiagnostics(result.issues)
    const bounded = boundedIssues(result.issues)
    const outcome = outcomeAtCommitBoundary(result.outcome, this.#downstreamCommitState)
    return {
      ...this.#baseResult(
        outcome,
        bounded,
        result.outcome === 'blocked' && this.#downstreamCommitState.canRetryUpstream(),
        []
      ),
      eventCategory: result.eventCategory,
      repairs: result.repairs
    }
  }

  inspectOpenAiSseEvent(event: ParsedOpenAIStreamEvent): CodexResponsesGuardSseResult {
    const eventData = event.data
    const response = plainObject(eventData?.response)
    const observedResponseId = stringValue(response?.id)
    if (!this.#responseResourceId && observedResponseId) {
      this.#responseResourceId = observedResponseId
    }
    return this.inspectParsedSse({
      responseResourceId: this.#responseResourceId ?? '',
      event: event.dataParseError ? undefined : (eventData ?? { type: event.eventType })
    })
  }

  observeCoverageGap(): CodexResponsesGuardSseResult {
    const issue: CodexProtocolIssue = {
      code: 'protocol_guard_coverage_degraded',
      message: 'Codex Responses contract issue: protocol_guard_coverage_degraded',
      path: [],
      provenance: 'unknown'
    }
    this.#recordDiagnostics([issue])
    const bounded = boundedIssues([issue])
    const outcome: CodexContractOutcome = 'observed_unknown'
    return {
      ...this.#baseResult(outcome, bounded, false, []),
      eventCategory: 'protocol_event',
      repairs: []
    }
  }

  recordAppliedSseRepairs(repairCount: number): void {
    if (repairCount <= 0 || this.#mode !== 'safe_repair') return
    this.#appliedRepairRuleIds.add('codex.r0.response.replace_stream_item_id')
    const outcome = this.#marker.checkpoint === 'gateway_bridge' ? 'repaired_bridge' : 'repaired_safe'
    this.#aggregateOutcome = higherPriorityOutcome(this.#aggregateOutcome, outcome)
  }

  snapshot(): CodexResponsesGuardSnapshot {
    return {
      revision: codexResponsesContractRevision,
      checkpoint: this.#marker.checkpoint,
      provenance: this.#marker.checkpoint,
      mode: this.#mode,
      outcome: this.#aggregateOutcome,
      retryable: this.#aggregateOutcome === 'blocked' && this.#downstreamCommitState.canRetryUpstream(),
      repairRuleIds: [...this.#appliedRepairRuleIds],
      diagnostics: this.#diagnostics.map(cloneIssue),
      omittedDiagnosticCount: this.#omittedDiagnosticCount,
      stream: this.#streamState.snapshot(),
      commit: commitSnapshot(this.#downstreamCommitState)
    }
  }

  dispose(): void {
    this.#streamState.dispose()
    this.#streamState = this.#createStreamState()
    this.#diagnostics.length = 0
    this.#omittedDiagnosticCount = 0
    this.#aggregateOutcome = 'clean'
    this.#responseResourceId = undefined
    this.#appliedRepairRuleIds.clear()
  }

  #jsonResult(
    value: JsonRecord,
    outcome: CodexResponsesGuardOutcome,
    bounded: { issues: readonly CodexProtocolIssue[]; omittedIssueCount: number },
    retryable: boolean,
    repairRuleIds: readonly string[]
  ): CodexResponsesGuardJsonResult {
    return {
      ...this.#baseResult(outcome, bounded, retryable, repairRuleIds),
      value
    }
  }

  #baseResult(
    outcome: CodexResponsesGuardOutcome,
    bounded: { issues: readonly CodexProtocolIssue[]; omittedIssueCount: number },
    retryable: boolean,
    repairRuleIds: readonly string[]
  ): CodexResponsesGuardResultBase {
    this.#aggregateOutcome = higherPriorityOutcome(this.#aggregateOutcome, outcome)
    return {
      revision: codexResponsesContractRevision,
      checkpoint: this.#marker.checkpoint,
      provenance: this.#marker.checkpoint,
      mode: this.#mode,
      outcome,
      issues: bounded.issues,
      omittedIssueCount: bounded.omittedIssueCount,
      retryable: outcome === 'late_violation' ? false : retryable,
      repairRuleIds,
      commit: commitSnapshot(this.#downstreamCommitState)
    }
  }

  #recordDiagnostics(issues: readonly CodexProtocolIssue[]): void {
    for (const issue of issues) {
      if (this.#diagnostics.length < codexResponsesGuardDiagnosticLimit) {
        this.#diagnostics.push(sanitizeIssue(issue))
      } else {
        this.#omittedDiagnosticCount += 1
      }
    }
  }

  #createStreamState(): ReturnType<typeof createCodexResponsesStreamContractState> {
    return createCodexResponsesStreamContractState({
      provenance: this.#marker.checkpoint,
      repairItemIds: this.#mode === 'safe_repair',
      createItemId: this.#createItemId
    })
  }
}

export function rewriteCodexResponsesSseEvent(
  event: ParsedOpenAIStreamEvent,
  repairs: readonly CodexStreamIdRepair[]
): Buffer | undefined {
  if (!event.data || repairs.length === 0) return undefined
  let data: JsonRecord = event.data
  let item: JsonRecord | undefined
  let response: JsonRecord | undefined
  let output: unknown[] | undefined
  for (const repair of repairs) {
    if (repair.field === 'item_id') {
      if (data === event.data) data = { ...data }
      data.item_id = repair.clientItemId
      continue
    }
    if (repair.field === 'item.id') {
      item ??= plainObject(data.item)
      if (!item) continue
      if (data === event.data) data = { ...data }
      item = { ...item, id: repair.clientItemId }
      data.item = item
      continue
    }
    response ??= plainObject(data.response)
    if (!response || !Array.isArray(response.output) || repair.outputIndex === undefined) continue
    output ??= [...response.output]
    const outputItem = plainObject(output[repair.outputIndex])
    if (!outputItem) continue
    output[repair.outputIndex] = { ...outputItem, id: repair.clientItemId }
    if (data === event.data) data = { ...data }
    response = { ...response, output }
    data.response = response
  }
  if (data === event.data) return undefined
  const eventLine = event.eventName ? `event: ${event.eventName}\n` : ''
  return Buffer.from(`${eventLine}data: ${JSON.stringify(data)}\n\n`, 'utf8')
}

export function createCodexResponsesResponseGuard(
  input: CreateCodexResponsesResponseGuardInput
): CodexResponsesResponseGuard {
  return new CodexResponsesResponseGuard(input)
}

function validateResponseEnvelope(
  response: JsonRecord,
  provenance: CodexResponsesGuardCheckpoint,
  envelopeKind: 'response' | 'compact'
): CodexContractValidationResult | undefined {
  if (envelopeKind === 'response' && response.object === 'response' && Array.isArray(response.output)) return undefined
  if (
    envelopeKind === 'compact'
    && Array.isArray(response.output)
    && (response.object === undefined || response.object === 'response.compaction')
  ) return undefined
  return {
    revision: codexResponsesContractRevision,
    outcome: 'blocked',
    issues: [diagnosticIssue('response_envelope_invalid', provenance, 'R2')]
  }
}

function outcomeAtCommitBoundary(
  outcome: CodexContractOutcome,
  downstreamCommitState: GatewayDownstreamCommitState
): CodexContractOutcome {
  if (
    downstreamCommitState.semanticCommitted
    && (outcome === 'repairable' || outcome === 'blocked')
  ) {
    return 'late_violation'
  }
  return outcome
}

function boundedIssues(issues: readonly CodexProtocolIssue[]): {
  issues: readonly CodexProtocolIssue[]
  omittedIssueCount: number
} {
  return {
    issues: issues.slice(0, codexResponsesGuardDiagnosticLimit).map(sanitizeIssue),
    omittedIssueCount: Math.max(0, issues.length - codexResponsesGuardDiagnosticLimit)
  }
}

function diagnosticIssue(
  code: string,
  provenance: CodexResponsesGuardCheckpoint,
  repairLevel: 'R2'
): CodexProtocolIssue {
  return {
    code,
    message: `Codex Responses contract issue: ${code}`,
    path: [],
    provenance,
    repairLevel
  }
}

function sanitizeIssue(issue: CodexProtocolIssue): CodexProtocolIssue {
  const code = boundedString(issue.code, 96)
  return {
    ...issue,
    code,
    message: `Codex Responses contract issue: ${code}`,
    path: issue.path.slice(0, 12).map((part) => typeof part === 'string' ? boundedString(part, 64) : part),
    itemType: issue.itemType === undefined ? undefined : boundedString(issue.itemType, 96)
  }
}

function cloneIssue(issue: CodexProtocolIssue): CodexProtocolIssue {
  return { ...issue, path: [...issue.path] }
}

function boundedString(value: string, maximumLength: number): string {
  return value.length <= maximumLength ? value : value.slice(0, maximumLength)
}

function commitSnapshot(state: GatewayDownstreamCommitState): CodexResponsesGuardCommitSnapshot {
  return {
    transportCommitted: state.transportCommitted,
    semanticCommitted: state.semanticCommitted,
    downstreamBytesWritten: state.downstreamBytesWritten
  }
}

function unique(values: readonly string[]): string[] {
  return [...new Set(values)]
}

function plainObject(value: unknown): JsonRecord | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as JsonRecord : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function higherPriorityOutcome(
  current: CodexResponsesGuardOutcome,
  next: CodexResponsesGuardOutcome
): CodexResponsesGuardOutcome {
  return outcomePriority(next) > outcomePriority(current) ? next : current
}

function outcomePriority(outcome: CodexResponsesGuardOutcome): number {
  switch (outcome) {
    case 'late_violation': return 7
    case 'blocked': return 6
    case 'repaired_bridge': return 5
    case 'repaired_safe': return 4
    case 'repairable': return 3
    case 'observed_unknown': return 2
    case 'clean': return 1
  }
}
