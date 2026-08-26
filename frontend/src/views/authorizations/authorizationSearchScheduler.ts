import type { Ref } from 'vue'

export interface AuthorizationSearchScheduler {
  schedule: (value: string) => void
  clear: () => void
}

interface AuthorizationSearchSchedulerOptions {
  delayMs: number
  keyword: Ref<string>
  load: (keyword: string) => Promise<void> | void
}

export function createAuthorizationSearchScheduler(options: AuthorizationSearchSchedulerOptions): AuthorizationSearchScheduler {
  const { delayMs, keyword, load } = options
  let timer: ReturnType<typeof window.setTimeout> | undefined

  function clear(): void {
    if (timer && typeof window !== 'undefined') {
      window.clearTimeout(timer)
      timer = undefined
    }
  }

  function schedule(value: string): void {
    keyword.value = value
    clear()
    timer = window.setTimeout(() => {
      timer = undefined
      void load(keyword.value)
    }, delayMs)
  }

  return { schedule, clear }
}
