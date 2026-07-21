import type {
  PageDataChangeProjection,
  PageDataConfirmDomainResult,
  PageDataConfirmRequest,
  PageDataConfirmResult,
  PageDataDomain,
  PageDataRevisionToken,
  PageDataViewScope
} from '@/api/domains/pageData'
import type { PageDataActivationManifest } from './pageDataActivationManifests'

export interface PageDataActivationParticipant {
  resourceKey: string
  domain: PageDataDomain
  token?: PageDataRevisionToken
  generation: number
  writeEpoch: number
}

export type PageDataActivationPhase = 'pre' | 'post'
export type PageDataActivationTriggerReason = 'mount' | 'activate' | 'focus' | 'interval'

export type PageDataActivationDecision =
  | {
      state: 'confirmed'
      phase: PageDataActivationPhase
      participant: PageDataActivationParticipant
      result: PageDataConfirmDomainResult & { serverTime: string }
    }
  | {
      state: 'token_conflict' | 'late' | 'superseded' | 'unavailable'
      phase: PageDataActivationPhase
      participant: PageDataActivationParticipant
    }

export interface PageDataActivationHandle {
  register(input: PageDataActivationParticipant): Promise<PageDataActivationDecision>
  stabilize(input: PageDataActivationParticipant & { baseline: PageDataRevisionToken }): Promise<PageDataActivationDecision>
  trigger(reason: PageDataActivationTriggerReason): void
  deactivate(): void
  dispose(): void
}

export interface PageDataActivationTimer {
  setTimeout(callback: () => void, delayMs: number): unknown
  clearTimeout(handle: unknown): void
}

export interface PageDataActivationCoordinatorOptions {
  manifest: PageDataActivationManifest
  viewScope: PageDataViewScope
  targetSystemAccountId?: string
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  batchWindowMs?: number
  timer?: PageDataActivationTimer
  now?: () => number
}

interface PendingDecision {
  participant: PageDataActivationParticipant
  resolve: (decision: PageDataActivationDecision) => void
  settled: boolean
}

interface DomainCollection {
  token: PageDataRevisionToken | null
  pending: PendingDecision[]
  conflicted: boolean
}

interface ActivationPhase {
  kind: PageDataActivationPhase
  activationId: number
  deadline: number
  state: 'collecting' | 'in_flight' | 'settled'
  domains: Map<PageDataDomain, DomainCollection>
  timerHandle?: unknown
}

interface Activation {
  id: number
  pre: ActivationPhase
  post?: ActivationPhase
}

const DEFAULT_BATCH_WINDOW_MS = 50

const defaultTimer: PageDataActivationTimer = {
  setTimeout: (callback, delayMs) => globalThis.setTimeout(callback, delayMs),
  clearTimeout: (handle) => globalThis.clearTimeout(handle as number)
}

export class PageDataActivationCoordinator implements PageDataActivationHandle {
  readonly manifest: PageDataActivationManifest

  private readonly confirm: PageDataActivationCoordinatorOptions['confirm']
  private readonly viewScope: PageDataViewScope
  private readonly targetSystemAccountId?: string
  private readonly batchWindowMs: number
  private readonly timer: PageDataActivationTimer
  private readonly now: () => number
  private nextActivationId = 0
  private active?: Activation
  private disposed = false

  constructor(options: PageDataActivationCoordinatorOptions) {
    this.manifest = options.manifest
    this.confirm = options.confirm
    this.viewScope = options.viewScope
    this.targetSystemAccountId = options.targetSystemAccountId
    this.batchWindowMs = Math.max(0, Math.min(Math.trunc(options.batchWindowMs ?? DEFAULT_BATCH_WINDOW_MS), 1_000))
    this.timer = options.timer ?? defaultTimer
    this.now = options.now ?? (() => Date.now())
  }

  register(input: PageDataActivationParticipant): Promise<PageDataActivationDecision> {
    const participant = cloneParticipant(input)
    const activation = this.active
    if (this.disposed || !activation) return Promise.resolve(decision('superseded', 'pre', participant))
    if (participant.token && participant.token.domain !== participant.domain) {
      return Promise.resolve(decision('token_conflict', 'pre', participant))
    }
    return this.collect(activation.pre, participant, participant.token ?? null)
  }

  stabilize(input: PageDataActivationParticipant & { baseline: PageDataRevisionToken }): Promise<PageDataActivationDecision> {
    const participant = cloneParticipant(input)
    const activation = this.active
    if (this.disposed || !activation) return Promise.resolve(decision('superseded', 'post', participant))
    if (input.baseline.domain !== participant.domain) {
      return Promise.resolve(decision('token_conflict', 'post', participant))
    }
    activation.post ??= this.createPhase('post', activation.id)
    return this.collect(activation.post, participant, cloneToken(input.baseline))
  }

  trigger(_reason: PageDataActivationTriggerReason): void {
    if (this.disposed) return
    if (this.active) this.supersede(this.active)
    const id = ++this.nextActivationId
    this.active = { id, pre: this.createPhase('pre', id) }
  }

  deactivate(): void {
    if (!this.active) return
    this.supersede(this.active)
    this.active = undefined
  }

  dispose(): void {
    if (this.disposed) return
    this.deactivate()
    this.disposed = true
  }

  private collect(
    phase: ActivationPhase,
    participant: PageDataActivationParticipant,
    token: PageDataRevisionToken | null
  ): Promise<PageDataActivationDecision> {
    if (phase.state === 'collecting' && this.now() >= phase.deadline) this.freeze(phase)
    if (phase.state !== 'collecting') return Promise.resolve(decision('late', phase.kind, participant))

    const current = phase.domains.get(participant.domain)
    if (current?.conflicted) return Promise.resolve(decision('token_conflict', phase.kind, participant))
    if (current && !sameOptionalToken(current.token, token)) {
      current.conflicted = true
      for (const pending of current.pending) this.settle(pending, decision('token_conflict', phase.kind, pending.participant))
      return Promise.resolve(decision('token_conflict', phase.kind, participant))
    }

    return new Promise<PageDataActivationDecision>((resolve) => {
      const pending: PendingDecision = { participant, resolve, settled: false }
      if (current) current.pending.push(pending)
      else phase.domains.set(participant.domain, { token: token ? cloneToken(token) : null, pending: [pending], conflicted: false })
    })
  }

  private createPhase(kind: PageDataActivationPhase, activationId: number): ActivationPhase {
    const phase: ActivationPhase = {
      kind,
      activationId,
      deadline: this.now() + this.batchWindowMs,
      state: 'collecting',
      domains: new Map()
    }
    phase.timerHandle = this.timer.setTimeout(() => this.freeze(phase), this.batchWindowMs)
    return phase
  }

  private freeze(phase: ActivationPhase): void {
    if (phase.state !== 'collecting') return
    if (phase.timerHandle !== undefined) {
      this.timer.clearTimeout(phase.timerHandle)
      phase.timerHandle = undefined
    }
    const domains = [...phase.domains.entries()].filter(([, collection]) => !collection.conflicted)
    if (domains.length === 0) {
      phase.state = 'settled'
      return
    }

    phase.state = 'in_flight'
    const request: PageDataConfirmRequest = {
      viewScope: this.viewScope,
      ...(this.targetSystemAccountId ? { targetSystemAccountId: this.targetSystemAccountId } : {}),
      domains: Object.fromEntries(domains.map(([domain, collection]) => [
        domain,
        collection.token ? cloneToken(collection.token) : null
      ]))
    }

    void Promise.resolve()
      .then(() => this.confirm(request))
      .then(
        (result) => this.complete(phase, domains, result),
        () => this.fail(phase, domains)
      )
  }

  private complete(
    phase: ActivationPhase,
    domains: Array<[PageDataDomain, DomainCollection]>,
    result: PageDataConfirmResult
  ): void {
    const current = this.isCurrent(phase)
    for (const [domain, collection] of domains) {
      const confirmed = result.domains[domain]
      for (const pending of collection.pending) {
        if (!current) {
          this.settle(pending, decision('superseded', phase.kind, pending.participant))
        } else if (!confirmed || confirmed.token.domain !== domain) {
          this.settle(pending, decision('unavailable', phase.kind, pending.participant))
        } else {
          this.settle(pending, {
            state: 'confirmed',
            phase: phase.kind,
            participant: cloneParticipant(pending.participant),
            result: cloneConfirmDomainResult(confirmed, result.serverTime)
          })
        }
      }
    }
    phase.state = 'settled'
  }

  private fail(phase: ActivationPhase, domains: Array<[PageDataDomain, DomainCollection]>): void {
    const state = this.isCurrent(phase) ? 'unavailable' : 'superseded'
    for (const [, collection] of domains) {
      for (const pending of collection.pending) this.settle(pending, decision(state, phase.kind, pending.participant))
    }
    phase.state = 'settled'
  }

  private isCurrent(phase: ActivationPhase): boolean {
    return !this.disposed && this.active?.id === phase.activationId
  }

  private supersede(activation: Activation): void {
    for (const phase of [activation.pre, activation.post]) {
      if (!phase) continue
      if (phase.timerHandle !== undefined) this.timer.clearTimeout(phase.timerHandle)
      phase.timerHandle = undefined
      for (const collection of phase.domains.values()) {
        for (const pending of collection.pending) {
          this.settle(pending, decision('superseded', phase.kind, pending.participant))
        }
      }
      phase.state = 'settled'
    }
  }

  private settle(pending: PendingDecision, value: PageDataActivationDecision): void {
    if (pending.settled) return
    pending.settled = true
    pending.resolve(value)
  }
}

export function createPageDataActivationCoordinator(
  options: PageDataActivationCoordinatorOptions
): PageDataActivationHandle {
  return new PageDataActivationCoordinator(options)
}

function decision(
  state: Exclude<PageDataActivationDecision['state'], 'confirmed'>,
  phase: PageDataActivationPhase,
  participant: PageDataActivationParticipant
): PageDataActivationDecision {
  return { state, phase, participant: cloneParticipant(participant) }
}

function sameOptionalToken(left: PageDataRevisionToken | null, right: PageDataRevisionToken | null): boolean {
  if (!left || !right) return left === right
  return left.protocolVersion === right.protocolVersion
    && left.epoch === right.epoch
    && left.scope === right.scope
    && left.domain === right.domain
    && left.sequence === right.sequence
    && left.resetSequence === right.resetSequence
}

function cloneParticipant(participant: PageDataActivationParticipant): PageDataActivationParticipant {
  return {
    ...participant,
    ...(participant.token ? { token: cloneToken(participant.token) } : {})
  }
}

function cloneToken(token: PageDataRevisionToken): PageDataRevisionToken {
  return { ...token }
}

function cloneConfirmDomainResult(
  result: PageDataConfirmDomainResult,
  serverTime: string
): PageDataConfirmDomainResult & { serverTime: string } {
  return {
    ...result,
    token: cloneToken(result.token),
    ...(result.changes ? { changes: result.changes.map(cloneProjection) } : {}),
    serverTime
  }
}

function cloneProjection(projection: PageDataChangeProjection): PageDataChangeProjection {
  return { ...projection, fieldMask: [...projection.fieldMask] }
}
