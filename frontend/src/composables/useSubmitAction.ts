import { computed, onBeforeUnmount, reactive } from 'vue'

type SubmitAction<TArgs extends unknown[], TResult> = (...args: TArgs) => Promise<TResult> | TResult

interface SubmitActionOptions {
  releaseDelayMs?: number
}

const globalPending = reactive(new Map<string, number>())

export function useSubmitAction(scope = 'local') {
  const ownedKeys = new Set<string>()

  function isSubmitting(key: string): boolean {
    return globalPending.has(resolveKey(scope, key))
  }

  function submitAction<TArgs extends unknown[], TResult>(
    key: string,
    action: SubmitAction<TArgs, TResult>,
    options: SubmitActionOptions = {}
  ): SubmitAction<TArgs, Promise<TResult | undefined>> {
    return async (...args: TArgs) => {
      const resolvedKey = resolveKey(scope, key)
      if (globalPending.has(resolvedKey)) {
        return undefined
      }

      globalPending.set(resolvedKey, Date.now())
      ownedKeys.add(resolvedKey)
      try {
        return await action(...args)
      } finally {
        const release = () => {
          globalPending.delete(resolvedKey)
          ownedKeys.delete(resolvedKey)
        }
        if (options.releaseDelayMs && options.releaseDelayMs > 0) {
          window.setTimeout(release, options.releaseDelayMs)
        } else {
          release()
        }
      }
    }
  }

  function submittingRef(key: string) {
    return computed(() => isSubmitting(key))
  }

  onBeforeUnmount(() => {
    for (const key of ownedKeys) {
      globalPending.delete(key)
    }
    ownedKeys.clear()
  })

  return {
    isSubmitting,
    submitAction,
    submittingRef
  }
}

function resolveKey(scope: string, key: string): string {
  return `${scope}:${key}`
}
