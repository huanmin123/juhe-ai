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

export interface PageDataActivation extends PageDataActivationHandle {
  registerRevalidator(domain: PageDataDomain, revalidate: () => void | Promise<void>): () => void
  beginTargeted(domains: readonly PageDataDomain[]): void
}

export interface UsePageDataActivationOptions {
  enabled: boolean
  manifest: PageDataActivationManifest
  viewScope: PageDataViewScope
  targetSystemAccountId?: string
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  batchWindowMs?: number
  intervalMs?: number
  timer?: PageDataActivationLifecycleTimer
  now?: () => number
  isVisible?: () => boolean
  addFocusListener?: (listener: () => void) => () => void
  addVisibilityListener?: (listener: () => void) => () => void
}

const DEFAULT_CONFIRM_INTERVAL_MS = 30_000

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
  const revalidators = new Map<PageDataDomain, Set<() => void | Promise<void>>>()
  const intervalMs = Math.max(1_000, Math.trunc(options.intervalMs ?? DEFAULT_CONFIRM_INTERVAL_MS))
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
  let pendingResume: { generation: number; reason: PageDataActivationTriggerReason } | undefined
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
    beginTargeted(domains) {
      if (disposed) return
      targetedDomains = new Set(domains.filter((domain) => manifestDomains.has(domain)))
      coordinator.trigger('activate')
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
    if (disposed || !componentActive || !running || !isVisible() || revalidationInFlight) return
    targetedDomains = undefined
    coordinator.trigger(reason)
    const pending = options.manifest.domains.flatMap((domain) =>
      [...(revalidators.get(domain) ?? [])].map((run) => runIsolated(run))
    )
    const operation = Promise.all(pending).then(() => undefined)
    revalidationInFlight = operation
    void operation.finally(() => {
      if (revalidationInFlight !== operation) return
      revalidationInFlight = undefined
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
}

function runIsolated(run: () => void | Promise<void>): Promise<void> {
  try {
    return Promise.resolve(run()).catch(() => undefined)
  } catch {
    return Promise.resolve()
  }
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
