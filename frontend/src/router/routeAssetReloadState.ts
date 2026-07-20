const routeAssetReloadStorageKey = 'juhe-ai:route-asset-reload'
const routeAssetReloadCooldownMs = 30_000

interface RouteAssetReloadRecord {
  path: string
  at: number
}

interface RouteAssetReloadStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): unknown
}

interface RouteAssetReloadStateOptions {
  storage?: RouteAssetReloadStorage
  now?: () => number
}

export function shouldReloadRouteAsset(
  path: string,
  options: RouteAssetReloadStateOptions = {}
): boolean {
  const lastReload = readRouteAssetReloadRecord(options)
  if (!lastReload) return true
  const now = options.now ?? Date.now
  return lastReload.path !== path || now() - lastReload.at > routeAssetReloadCooldownMs
}

export function markRouteAssetReload(
  path: string,
  options: RouteAssetReloadStateOptions = {}
): boolean {
  try {
    const storage = options.storage ?? window.sessionStorage
    const now = options.now ?? Date.now
    storage.setItem(routeAssetReloadStorageKey, JSON.stringify({ path, at: now() }))
    return true
  } catch {
    return false
  }
}

function readRouteAssetReloadRecord(
  options: RouteAssetReloadStateOptions
): RouteAssetReloadRecord | undefined {
  try {
    const storage = options.storage ?? window.sessionStorage
    const text = storage.getItem(routeAssetReloadStorageKey)
    if (!text) return undefined
    const value = JSON.parse(text) as Partial<RouteAssetReloadRecord>
    if (typeof value.path !== 'string' || typeof value.at !== 'number') return undefined
    return { path: value.path, at: value.at }
  } catch {
    return undefined
  }
}
