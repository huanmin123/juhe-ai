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

export async function loadAuthorizationOptionResource<T>(options: AuthorizationOptionResourceOptions<T>): Promise<T> {
  const result = await options.loadNetwork()
  applyIfCurrent(options, result)
  return result
}

function applyIfCurrent<T>(options: AuthorizationOptionResourceOptions<T>, value: T): void {
  if (options.isCurrent()) options.apply(value)
}
