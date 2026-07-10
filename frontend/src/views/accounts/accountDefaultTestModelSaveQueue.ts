import type { AccountSummary } from '@/types/domain'

export type AccountDefaultTestModelApplyPhase = 'optimistic' | 'persisted' | 'rollback'

interface AccountDefaultTestModelSaveState {
  account: AccountSummary
  appliedModel: string | undefined
  applyPhase: AccountDefaultTestModelApplyPhase
  desiredModel: string | undefined
  desiredRevision: number
  persistedModel: string | undefined
  requiresWrite: boolean
  options: AccountDefaultTestModelSaveQueueOptions
  running?: Promise<void>
}

interface AccountDefaultTestModelSaveQueueOptions {
  apply?: (accountId: string, model: string | undefined, phase: AccountDefaultTestModelApplyPhase) => void
  persist: (account: AccountSummary, model: string) => Promise<string>
  onLatestFailure: (error: unknown) => void
  onSupersededFailure?: (error: unknown) => void
}

export function createAccountDefaultTestModelSaveQueue(defaultOptions?: AccountDefaultTestModelSaveQueueOptions) {
  const states = new Map<string, AccountDefaultTestModelSaveState>()
  const listeners = new Set<(
    accountId: string,
    model: string | undefined,
    phase: AccountDefaultTestModelApplyPhase
  ) => void>()

  function enqueue(
    account: AccountSummary,
    model: string,
    queueOptions = defaultOptions
  ): void {
    const normalizedModel = model.trim()
    if (!normalizedModel || !queueOptions) return
    const persistedModel = account.defaultTestModel?.trim() || undefined
    const state = states.get(account.id) ?? {
      account,
      appliedModel: persistedModel,
      applyPhase: 'persisted' as const,
      desiredModel: persistedModel,
      desiredRevision: 0,
      persistedModel,
      requiresWrite: false,
      options: queueOptions
    }
    state.account = account
    state.desiredModel = normalizedModel
    state.desiredRevision += 1
    state.requiresWrite = true
    state.options = queueOptions
    states.set(account.id, state)
    notify(state, normalizedModel, 'optimistic')
    startDrain(state)
  }

  function startDrain(state: AccountDefaultTestModelSaveState): void {
    if (state.running) return
    const running = drain(state).finally(() => {
      if (states.get(state.account.id) !== state || state.running !== running) return
      state.running = undefined
      if (state.desiredModel === state.persistedModel) {
        states.delete(state.account.id)
        return
      }
      startDrain(state)
    })
    state.running = running
  }

  async function drain(state: AccountDefaultTestModelSaveState): Promise<void> {
    while (state.desiredModel && (state.requiresWrite || state.desiredModel !== state.persistedModel)) {
      const attemptedModel = state.desiredModel
      const attemptedRevision = state.desiredRevision
      const attemptOptions = state.options
      try {
        const savedModel = (await attemptOptions.persist(state.account, attemptedModel)).trim() || attemptedModel
        state.persistedModel = savedModel
        if (state.desiredRevision === attemptedRevision) {
          state.desiredModel = savedModel
          state.requiresWrite = false
          notify(state, savedModel, 'persisted')
        }
      } catch (error) {
        if (state.desiredRevision === attemptedRevision) {
          state.desiredModel = state.persistedModel
          state.requiresWrite = false
          notify(state, state.persistedModel, 'rollback')
          state.options.onLatestFailure(error)
        } else {
          state.requiresWrite = true
          state.options.onSupersededFailure?.(error)
        }
      }
    }
  }

  async function whenIdle(accountId: string): Promise<void> {
    while (true) {
      const state = states.get(accountId)
      if (!state) return
      if (!state.running) {
        startDrain(state)
      }
      await state.running
    }
  }

  function notify(
    state: AccountDefaultTestModelSaveState,
    model: string | undefined,
    phase: AccountDefaultTestModelApplyPhase
  ): void {
    state.appliedModel = model
    state.applyPhase = phase
    state.options.apply?.(state.account.id, model, phase)
    for (const listener of listeners) {
      listener(state.account.id, model, phase)
    }
  }

  function subscribe(
    listener: (
      accountId: string,
      model: string | undefined,
      phase: AccountDefaultTestModelApplyPhase
    ) => void
  ): () => void {
    listeners.add(listener)
    for (const state of states.values()) {
      listener(state.account.id, state.appliedModel, state.applyPhase)
    }
    return () => {
      listeners.delete(listener)
    }
  }

  return {
    enqueue,
    subscribe,
    whenIdle
  }
}

export const accountDefaultTestModelSaveQueue = createAccountDefaultTestModelSaveQueue()
