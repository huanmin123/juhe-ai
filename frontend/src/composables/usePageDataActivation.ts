import { onActivated, onDeactivated, onMounted, onUnmounted } from 'vue'

import type {
  PageDataConfirmRequest,
  PageDataConfirmResult,
  PageDataDomain,
  PageDataViewScope
} from '@/api/domains/pageData'
import {
  createPageDataActivationCoordinator,
  type PageDataActivationDecision,
  type PageDataActivationHandle,
  type PageDataActivationParticipant,
  type PageDataActivationTimer,
  type PageDataActivationTriggerReason
} from '@/shared/pageDataActivationCoordinator'
import type { PageDataActivationManifest } from '@/shared/pageDataActivationManifests'

export type PageDataActivationLifecycleTimer = PageDataActivationTimer

type PageDataRevalidator = (activation: PageDataActivationHandle) => void | Promise<void>

export interface PageDataActivation extends PageDataActivationHandle {
  registerRevalidator(domain: PageDataDomain, revalidate: PageDataRevalidator): () => void
  runTargeted<T>(
    domains: readonly PageDataDomain[],
    run: (activation: PageDataActivationHandle) => Promise<T>
  ): Promise<T>
}

export interface UsePageDataActivationOptions {
  enabled: boolean
  manifest: PageDataActivationManifest
  viewScope: PageDataViewScope
  targetSystemAccountId?: string
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  batchWindowMs?: number
  intervalMs?: number
  revalidationTimeoutMs?: number
  timer?: PageDataActivationLifecycleTimer
  now?: () => number
  isVisible?: () => boolean
  addFocusListener?: (listener: () => void) => () => void
  addVisibilityListener?: (listener: () => void) => () => void
}

const DEFAULT_CONFIRM_INTERVAL_MS = 30_000
const DEFAULT_REVALIDATION_TIMEOUT_MS = 20_000

const defaultTimer: PageDataActivationLifecycleTimer = {
  setTimeout: (callback, delayMs) => globalThis.setTimeout(callback, delayMs),
  clearTimeout: (handle) => globalThis.clearTimeout(handle as number)
}

export function usePageDataActivation(options: UsePageDataActivationOptions): PageDataActivation | undefined {
  if (!options.enabled) return undefined

  const timer = options.timer ?? defaultTimer
  const coordinator = createPageDataActivationCoordinator({
    manifest: options.manifest,
    viewScope: options.viewScope,
    ...(options.targetSystemAccountId ? { targetSystemAccountId: options.targetSystemAccountId } : {}),
    confirm: options.confirm,
    batchWindowMs: options.batchWindowMs,
    timer,
    now: options.now
  })
  const manifestDomains = new Set(options.manifest.domains)
  const revalidators = new Map<PageDataDomain, Set<PageDataRevalidator>>()
  const intervalMs = Math.max(1_000, Math.trunc(options.intervalMs ?? DEFAULT_CONFIRM_INTERVAL_MS))
  const revalidationTimeoutMs = Math.max(
    100,
    Math.min(Math.trunc(options.revalidationTimeoutMs ?? DEFAULT_REVALIDATION_TIMEOUT_MS), 60_000)
  )
  const isVisible = options.isVisible ?? (() => typeof document === 'undefined' || document.visibilityState !== 'hidden')
  const addFocusListener = options.addFocusListener ?? defaultFocusListener
  const addVisibilityListener = options.addVisibilityListener ?? defaultVisibilityListener

  let componentActive = true
  let running = false
  let disposed = false
  let targetedDomains: ReadonlySet<PageDataDomain> | undefined
  let intervalHandle: unknown
  let revalidationInFlight: Promise<void> | undefined
  let activationGeneration = 0
  let scopeGeneration = 0
  let pendingResume: { generation: number; reason: PageDataActivationTriggerReason } | undefined
  let targetedRequests = 0
  let targetedQueue: Promise<void> = Promise.resolve()
  let removeFocusListener: (() => void) | undefined
  let removeVisibilityListener: (() => void) | undefined

  const activation: PageDataActivation = {
    register(input) {
      if (!isTargetDomain(input.domain)) return Promise.resolve(unavailableDecision('pre', input))
      return coordinator.register(input)
    },
    stabilize(input) {
      if (!isTargetDomain(input.domain)) return Promise.resolve(unavailableDecision('post', input))
      return coordinator.stabilize(input)
    },
    trigger(reason) {
      revalidate(reason)
    },
    deactivate,
    dispose,
    registerRevalidator(domain, revalidate) {
      if (disposed || !manifestDomains.has(domain)) return () => undefined
      let domainRevalidators = revalidators.get(domain)
      if (!domainRevalidators) {
        domainRevalidators = new Set()
        revalidators.set(domain, domainRevalidators)
      }
      domainRevalidators.add(revalidate)
      return () => {
        domainRevalidators?.delete(revalidate)
        if (domainRevalidators?.size === 0) revalidators.delete(domain)
      }
    },
    runTargeted<T>(
      domains: readonly PageDataDomain[],
      run: (activation: PageDataActivationHandle) => Promise<T>
    ): Promise<T> {
      targetedRequests += 1
      const operation = targetedQueue.then(async () => {
        const pendingFullRevalidation = revalidationInFlight
        if (pendingFullRevalidation) await pendingFullRevalidation
        if (disposed || !componentActive || !running || !isVisible()) {
          throw new Error('页面数据 activation 当前不可用')
        }
        const allowedDomains = new Set(domains.filter((domain) => manifestDomains.has(domain)))
        targetedDomains = allowedDomains
        const generation = ++scopeGeneration
        coordinator.trigger('activate')
        try {
          return await run(scopedHandle(generation, allowedDomains))
        } finally {
          if (scopeGeneration === generation) scopeGeneration += 1
          targetedDomains = undefined
          coordinator.deactivate()
        }
      })
      targetedQueue = operation.then(
        () => undefined,
        () => undefined
      ).finally(() => {
        targetedRequests -= 1
        if (targetedRequests !== 0) return
        const resumeRequest = pendingResume
        pendingResume = undefined
        if (
          resumeRequest?.generation === activationGeneration &&
          !disposed && componentActive && running && isVisible()
        ) {
          revalidate(resumeRequest.reason)
          return
        }
        scheduleInterval()
      })
      return operation
    }
  }

  onMounted(() => {
    removeFocusListener = addFocusListener(handleFocus)
    removeVisibilityListener = addVisibilityListener(handleVisibility)
    resume('mount')
  })
  onActivated(() => {
    componentActive = true
    resume('activate')
  })
  onDeactivated(deactivate)
  onUnmounted(dispose)

  return activation

  function resume(reason: PageDataActivationTriggerReason): void {
    if (disposed || !componentActive || running || !isVisible()) return
    running = true
    if (revalidationInFlight) {
      pendingResume = { generation: activationGeneration, reason }
      return
    }
    revalidate(reason)
  }

  function revalidate(reason: PageDataActivationTriggerReason): void {
    if (
      disposed
      || !componentActive
      || !running
      || !isVisible()
      || revalidationInFlight
      || targetedRequests > 0
    ) return
    targetedDomains = undefined
    const generation = ++scopeGeneration
    coordinator.trigger(reason)
    const scopedActivation = scopedHandle(generation)
    const pending = options.manifest.domains.flatMap((domain) =>
      [...(revalidators.get(domain) ?? [])].map((run) => runIsolated(() => run(scopedActivation)))
    )
    const operation = settleWithin(
      Promise.all(pending).then(() => undefined),
      revalidationTimeoutMs,
      timer,
      () => {
        if (scopeGeneration === generation) scopeGeneration += 1
        coordinator.deactivate()
      }
    )
    revalidationInFlight = operation
    void operation.finally(() => {
      if (revalidationInFlight !== operation) return
      revalidationInFlight = undefined
      if (targetedRequests > 0) return
      const resumeRequest = pendingResume
      pendingResume = undefined
      if (
        resumeRequest?.generation === activationGeneration &&
        !disposed && componentActive && running && isVisible()
      ) {
        revalidate(resumeRequest.reason)
        return
      }
      scheduleInterval()
    })
  }

  function deactivate(): void {
    componentActive = false
    pause()
  }

  function pause(): void {
    running = false
    activationGeneration += 1
    scopeGeneration += 1
    pendingResume = undefined
    targetedDomains = undefined
    stopIntervalTimeout()
    coordinator.deactivate()
  }

  function dispose(): void {
    if (disposed) return
    deactivate()
    disposed = true
    removeFocusListener?.()
    removeVisibilityListener?.()
    removeFocusListener = undefined
    removeVisibilityListener = undefined
    revalidators.clear()
    coordinator.dispose()
  }

  function scheduleInterval(): void {
    if (disposed || !componentActive || !running || !isVisible() || intervalHandle !== undefined) return
    intervalHandle = timer.setTimeout(() => {
      intervalHandle = undefined
      if (!componentActive || !isVisible()) {
        pause()
        return
      }
      revalidate('interval')
    }, intervalMs)
  }

  function stopIntervalTimeout(): void {
    if (intervalHandle === undefined) return
    timer.clearTimeout(intervalHandle)
    intervalHandle = undefined
  }

  function handleFocus(): void {
    if (!componentActive || !isVisible()) return
    if (!running) {
      resume('focus')
      return
    }
    revalidate('focus')
  }

  function handleVisibility(): void {
    if (!isVisible()) {
      pause()
      return
    }
    if (!componentActive) return
    if (!running) {
      resume('focus')
      return
    }
    revalidate('focus')
  }

  function isTargetDomain(domain: PageDataDomain): boolean {
    return !targetedDomains || targetedDomains.has(domain)
  }

  function scopedHandle(
    generation: number,
    allowedDomains: ReadonlySet<PageDataDomain> = manifestDomains
  ): PageDataActivationHandle {
    const isCurrent = () => generation === scopeGeneration && !disposed && componentActive && running
    return {
      register(input) {
        if (!isCurrent()) return Promise.resolve(supersededDecision('pre', input))
        if (!allowedDomains.has(input.domain)) return Promise.resolve(unavailableDecision('pre', input))
        return coordinator.register(input)
      },
      stabilize(input) {
        if (!isCurrent()) return Promise.resolve(supersededDecision('post', input))
        if (!allowedDomains.has(input.domain)) return Promise.resolve(unavailableDecision('post', input))
        return coordinator.stabilize(input)
      },
      trigger: () => undefined,
      deactivate: () => undefined,
      dispose: () => undefined
    }
  }
}

function runIsolated(run: () => void | Promise<void>): Promise<void> {
  try {
    return Promise.resolve(run()).catch(() => undefined)
  } catch {
    return Promise.resolve()
  }
}

function settleWithin(
  operation: Promise<void>,
  timeoutMs: number,
  timer: PageDataActivationLifecycleTimer,
  onTimeout: () => void
): Promise<void> {
  return new Promise((resolve) => {
    let settled = false
    let deadlineHandle: unknown
    const settle = () => {
      if (settled) return
      settled = true
      if (deadlineHandle !== undefined) timer.clearTimeout(deadlineHandle)
      resolve()
    }
    deadlineHandle = timer.setTimeout(() => {
      onTimeout()
      settle()
    }, timeoutMs)
    void operation.then(settle, settle)
  })
}

function defaultFocusListener(listener: () => void): () => void {
  if (typeof window === 'undefined') return () => undefined
  window.addEventListener('focus', listener)
  return () => window.removeEventListener('focus', listener)
}

function defaultVisibilityListener(listener: () => void): () => void {
  if (typeof document === 'undefined') return () => undefined
  document.addEventListener('visibilitychange', listener)
  return () => document.removeEventListener('visibilitychange', listener)
}

function unavailableDecision(
  phase: PageDataActivationDecision['phase'],
  participant: PageDataActivationParticipant
): PageDataActivationDecision {
  return {
    state: 'unavailable',
    phase,
    participant: {
      ...participant,
      ...(participant.token ? { token: { ...participant.token } } : {})
    }
  }
}

function supersededDecision(
  phase: PageDataActivationDecision['phase'],
  participant: PageDataActivationParticipant
): PageDataActivationDecision {
  return {
    state: 'superseded',
    phase,
    participant: {
      ...participant,
      ...(participant.token ? { token: { ...participant.token } } : {})
    }
  }
}
