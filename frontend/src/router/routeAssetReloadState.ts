const routeAssetReloadStorageKey = 'juhe-ai:route-asset-reload'
const routeAssetReloadCooldownMs = 30_000

type RouteAssetReloadRecords = Record<string, number>

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
  const reloads = readRouteAssetReloadRecords(options)
  if (reloads === null) return false
  const lastReloadAt = reloads[path]
  if (lastReloadAt === undefined) return true
  const now = options.now ?? Date.now
  return now() - lastReloadAt > routeAssetReloadCooldownMs
}

export function markRouteAssetReload(
  path: string,
  options: RouteAssetReloadStateOptions = {}
): boolean {
  try {
    const storage = options.storage ?? window.sessionStorage
    const now = options.now ?? Date.now
    const reloads = readRouteAssetReloadRecords(options)
    if (reloads === null) return false
    reloads[path] = now()
    storage.setItem(routeAssetReloadStorageKey, JSON.stringify(reloads))
    return true
  } catch {
    return false
  }
}

function readRouteAssetReloadRecords(
  options: RouteAssetReloadStateOptions
): RouteAssetReloadRecords | null {
  let text: string | null
  try {
    const storage = options.storage ?? window.sessionStorage
    text = storage.getItem(routeAssetReloadStorageKey)
  } catch {
    return null
  }
  if (!text) return {}

  try {
    const value = JSON.parse(text) as unknown
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
    if ('path' in value && 'at' in value && typeof value.path === 'string' && typeof value.at === 'number') {
      return { [value.path]: value.at }
    }
    const reloads: RouteAssetReloadRecords = {}
    for (const [path, at] of Object.entries(value)) {
      if (typeof at === 'number') reloads[path] = at
    }
    return reloads
  } catch {
    return {}
  }
}
