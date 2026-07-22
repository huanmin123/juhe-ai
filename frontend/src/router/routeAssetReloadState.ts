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
  const lastReloadAt = reloads[normalizeRouteAssetReloadKey(path)]
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
    const currentTime = now()
    for (const [reloadPath, lastReloadAt] of Object.entries(reloads)) {
      if (currentTime - lastReloadAt > routeAssetReloadCooldownMs) {
        delete reloads[reloadPath]
      }
    }
    reloads[normalizeRouteAssetReloadKey(path)] = currentTime
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
      return { [normalizeRouteAssetReloadKey(value.path)]: value.at }
    }
    const reloads: RouteAssetReloadRecords = {}
    for (const [path, at] of Object.entries(value)) {
      if (typeof at !== 'number') continue
      const reloadPath = normalizeRouteAssetReloadKey(path)
      reloads[reloadPath] = Math.max(reloads[reloadPath] ?? Number.NEGATIVE_INFINITY, at)
    }
    return reloads
  } catch {
    return {}
  }
}

function normalizeRouteAssetReloadKey(path: string): string {
  const pathname = path.split(/[?#]/, 1)[0]?.trim()
  return pathname || '/'
}
