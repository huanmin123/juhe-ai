import { onActivated, onDeactivated } from 'vue'

export type SupersededResourceState = 'ready' | 'superseded'

export function useKeepAliveSupersededRecovery(recover: () => void | Promise<void>) {
  let active = false
  let activationGeneration = 0
  let attemptedGeneration = -1
  let pending = false
  let recoveryInFlight = false
  let recoveryScheduled = false
  let latestRequest = 0

  onActivated(() => {
    active = true
    activationGeneration += 1
    scheduleRecovery()
  })

  onDeactivated(() => {
    active = false
  })

  function start(): number {
    pending = false
    return ++latestRequest
  }

  function record(request: number, state: SupersededResourceState): void {
    if (request !== latestRequest) return
    pending = state === 'superseded'
    if (pending) scheduleRecovery()
  }

  function scheduleRecovery(): void {
    if (!active || !pending || recoveryInFlight || recoveryScheduled || attemptedGeneration === activationGeneration) return
    recoveryScheduled = true
    queueMicrotask(() => {
      recoveryScheduled = false
      if (!active || !pending || recoveryInFlight || attemptedGeneration === activationGeneration) return
      attemptedGeneration = activationGeneration
      pending = false
      recoveryInFlight = true
      void Promise.resolve()
        .then(recover)
        .catch((error) => { console.error(error) })
        .finally(() => {
          recoveryInFlight = false
          scheduleRecovery()
        })
    })
  }

  return { start, record }
}
