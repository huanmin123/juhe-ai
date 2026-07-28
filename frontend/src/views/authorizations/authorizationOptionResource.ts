interface AuthorizationOptionResourceOptions<T> {
  apply: (options: T) => void
  domain: string
  isCurrent: () => boolean
  isManagementView: boolean
  loadNetwork: () => Promise<T>
  query: unknown
  route: string
  targetSystemAccountId?: string
}

export interface AuthorizationOptionSingleflight {
  invalidate: () => void
  run: <T>(key: string, load: () => Promise<T>) => Promise<T>
}

export function createAuthorizationOptionSingleflight(): AuthorizationOptionSingleflight {
  const pending = new Map<string, Promise<unknown>>()

  function invalidate(): void {
    pending.clear()
  }

  function run<T>(key: string, load: () => Promise<T>): Promise<T> {
    const active = pending.get(key) as Promise<T> | undefined
    if (active) return active

    const request = Promise.resolve().then(load)
    pending.set(key, request)
    void request.finally(() => {
      if (pending.get(key) === request) pending.delete(key)
    }).catch(() => undefined)
    return request
  }

  return { invalidate, run }
}

export async function loadAuthorizationOptionResource<T>(options: AuthorizationOptionResourceOptions<T>): Promise<T> {
  const result = await options.loadNetwork()
  applyIfCurrent(options, result)
  return result
}

function applyIfCurrent<T>(options: AuthorizationOptionResourceOptions<T>, value: T): void {
  if (options.isCurrent()) options.apply(value)
}
