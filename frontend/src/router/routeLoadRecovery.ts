import type { Router } from 'vue-router'

import { message } from '@/lib/antd'
import { classifyFrontendBuild, loadRemoteFrontendBuildId } from './frontendBuildInfo'

const routeAssetReloadStorageKey = 'juhe-ai:route-asset-reload'
const routeAssetReloadCooldownMs = 30_000
const routeAssetReloadDelayMs = 900
const routeAssetOverlayId = 'juhe-ai-route-asset-reload-overlay'
let routeAssetReloadScheduled = false

interface RouteAssetReloadRecord {
  path: string
  at: number
}

const routeAssetLoadErrorPatterns = [
  /failed to fetch dynamically imported module/i,
  /error loading dynamically imported module/i,
  /importing a module script failed/i,
  /chunkloaderror/i,
  /loading chunk [\w-]+ failed/i,
  /unable to preload css/i,
  /css_chunk_load_failed/i
]

export function installRouteLoadRecovery(router: Router): void {
  window.addEventListener('vite:preloadError', (event) => {
    const preloadEvent = event as Event & { payload?: unknown }
    if (recoverRouteAssetLoadError(preloadEvent.payload, router)) {
      event.preventDefault()
    }
  })

  router.onError((error, to) => {
    if (recoverRouteAssetLoadError(error, router, to?.fullPath)) return

    console.error(error)
    message.error('页面加载失败，请刷新后重试')
  })

  window.addEventListener('unhandledrejection', (event) => {
    if (recoverRouteAssetLoadError(event.reason, router)) {
      event.preventDefault()
    }
  })
}

export function recoverRouteAssetLoadError(error: unknown, router: Router, targetPath?: string): boolean {
  if (!isRouteAssetLoadError(error)) return false
  if (routeAssetReloadScheduled) return true

  const reloadPath = targetPath || router.currentRoute.value.fullPath || '/'
  if (!shouldReloadRouteAsset(reloadPath)) {
    console.error(error)
    message.error('页面资源加载失败，请手动刷新页面后重试')
    showRouteAssetLoadOverlay({
      title: '页面资源加载失败',
      description: '自动恢复未成功，请手动刷新页面后重试。',
      actionLabel: '刷新页面',
      onAction: () => window.location.reload()
    })
    return true
  }

  routeAssetReloadScheduled = true
  markRouteAssetReload(reloadPath)
  const reloadHref = router.resolve(reloadPath).href
  void showRouteAssetRecoveryAndReload(reloadHref, error).catch((recoveryError) => {
    console.error('页面资源自动恢复失败，正在直接重新加载。', recoveryError)
    window.location.assign(reloadHref)
  })
  return true
}

async function showRouteAssetRecoveryAndReload(reloadHref: string, originalError: unknown): Promise<void> {
  const status = await classifyFrontendBuild(
    __JUHE_AI_FRONTEND_BUILD_ID__,
    () => loadRemoteFrontendBuildId()
  )
  const updated = status === 'changed'

  console.warn('页面资源加载失败，正在刷新前端入口。', originalError)
  message.warning(updated ? '系统前端已更新，正在刷新页面' : '页面资源加载失败，正在重新加载页面')
  showRouteAssetLoadOverlay({
    title: updated ? '系统已更新' : '页面资源加载失败',
    description: updated
      ? '正在刷新页面以加载最新版本，请稍候。'
      : '正在重新加载页面，请稍候。',
    actionLabel: updated ? '立即刷新' : '立即重新加载',
    onAction: () => window.location.assign(reloadHref)
  })
  window.setTimeout(() => {
    window.location.assign(reloadHref)
  }, routeAssetReloadDelayMs)
}

function showRouteAssetLoadOverlay(options: {
  title: string
  description: string
  actionLabel: string
  onAction: () => void
}): void {
  const overlay = document.getElementById(routeAssetOverlayId) ?? document.createElement('div')
  overlay.id = routeAssetOverlayId
  overlay.style.cssText = [
    'position:fixed',
    'inset:0',
    'z-index:2147483647',
    'display:flex',
    'align-items:center',
    'justify-content:center',
    'padding:24px',
    'background:rgba(15,23,42,0.48)'
  ].join(';')

  const panel = document.createElement('div')
  panel.style.cssText = [
    'width:min(420px,100%)',
    'padding:24px',
    'border-radius:8px',
    'background:#fff',
    'box-shadow:0 20px 48px rgba(15,23,42,0.22)',
    'color:#0f172a',
    'font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif',
    'text-align:left'
  ].join(';')

  const title = document.createElement('div')
  title.textContent = options.title
  title.style.cssText = 'font-size:18px;font-weight:700;line-height:26px;margin-bottom:8px'

  const description = document.createElement('div')
  description.textContent = options.description
  description.style.cssText = 'font-size:14px;line-height:22px;color:#475569;margin-bottom:18px'

  const button = document.createElement('button')
  button.type = 'button'
  button.textContent = options.actionLabel
  button.style.cssText = [
    'height:36px',
    'padding:0 16px',
    'border:0',
    'border-radius:6px',
    'background:#1677ff',
    'color:#fff',
    'font-size:14px',
    'font-weight:600',
    'cursor:pointer'
  ].join(';')
  button.addEventListener('click', options.onAction, { once: true })

  panel.replaceChildren(title, description, button)
  overlay.replaceChildren(panel)
  if (!overlay.parentElement) {
    document.body.appendChild(overlay)
  }
}

function isRouteAssetLoadError(error: unknown): boolean {
  const text = routeErrorText(error)
  if (!text) return false
  if (routeAssetLoadErrorPatterns.some((pattern) => pattern.test(text))) return true
  return /(^|\/)assets\/.+\.(js|css)(\?|$)/i.test(text) && /fail|error|load/i.test(text)
}

function routeErrorText(error: unknown): string {
  if (error instanceof Error) {
    return [error.name, error.message, error.stack].filter(Boolean).join('\n')
  }
  if (typeof error === 'string') return error
  if (typeof error === 'object' && error !== null) {
    const value = error as { name?: unknown; message?: unknown; stack?: unknown }
    return [value.name, value.message, value.stack]
      .filter((item): item is string => typeof item === 'string' && item.length > 0)
      .join('\n')
  }
  return ''
}

function shouldReloadRouteAsset(path: string): boolean {
  const lastReload = readRouteAssetReloadRecord()
  if (!lastReload) return true
  return lastReload.path !== path || Date.now() - lastReload.at > routeAssetReloadCooldownMs
}

function markRouteAssetReload(path: string): void {
  try {
    window.sessionStorage.setItem(routeAssetReloadStorageKey, JSON.stringify({ path, at: Date.now() }))
  } catch {
    // sessionStorage may be disabled; reloading once is still the correct recovery path.
  }
}

function readRouteAssetReloadRecord(): RouteAssetReloadRecord | undefined {
  try {
    const text = window.sessionStorage.getItem(routeAssetReloadStorageKey)
    if (!text) return undefined
    const value = JSON.parse(text) as Partial<RouteAssetReloadRecord>
    if (typeof value.path !== 'string' || typeof value.at !== 'number') return undefined
    return { path: value.path, at: value.at }
  } catch {
    return undefined
  }
}
