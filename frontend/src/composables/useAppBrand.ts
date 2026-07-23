import { reactive } from 'vue'

import { api } from '@/api/client'
import type { GlobalSettings } from '@/types/domain'

export interface AppBrandSettings {
  appName: string
  appIcon: string
}

export const defaultAppBrand: AppBrandSettings = {
  appName: '聚合 AI',
  appIcon: '/__aisys__/brand-icon.svg'
}

export const appBrand = reactive<AppBrandSettings>({ ...defaultAppBrand })

export interface AppBrandSettingsResource<T> {
  load: () => Promise<T>
  set: (value: T) => T
  clear: () => void
}

export function createAppBrandSettingsResource<T>(options: {
  fetch: () => Promise<T>
  commit: (value: T) => void
}): AppBrandSettingsResource<T> {
  let cached: T | undefined
  let inFlight: Promise<T> | undefined
  let revision = 0

  return {
    async load() {
      if (cached !== undefined) return cached
      if (inFlight) return inFlight
      const requestRevision = revision
      const request = options.fetch().then((value) => {
        if (requestRevision !== revision) return cached ?? value
        cached = value
        options.commit(value)
        return value
      }).finally(() => {
        if (inFlight === request) inFlight = undefined
      })
      inFlight = request
      return request
    },
    set(value) {
      revision += 1
      cached = value
      options.commit(value)
      return value
    },
    clear() {
      revision += 1
      cached = undefined
      inFlight = undefined
    }
  }
}

const appBrandSettingsResource = createAppBrandSettingsResource<GlobalSettings>({
  fetch: () => api.settings.public(),
  commit: applyAppBrandValue
})

export function normalizeAppBrand(settings: Pick<GlobalSettings, 'appName' | 'appIcon'>): AppBrandSettings {
  return {
    appName: stringValue(settings.appName, defaultAppBrand.appName),
    appIcon: stringValue(settings.appIcon, defaultAppBrand.appIcon)
  }
}

export function applyAppBrand(settings: Pick<GlobalSettings, 'appName' | 'appIcon'>): AppBrandSettings {
  const next = normalizeAppBrand(settings)
  appBrandSettingsResource.set(next)
  return next
}

function applyAppBrandValue(settings: Pick<GlobalSettings, 'appName' | 'appIcon'>): void {
  const next = normalizeAppBrand(settings)
  appBrand.appName = next.appName
  appBrand.appIcon = next.appIcon
  syncDocumentBrand(next.appIcon)
  syncDocumentTitle()
}

export function syncDocumentTitle(pageTitle?: string): void {
  const title = pageTitle?.trim()
  document.title = title && title !== appBrand.appName ? `${title} - ${appBrand.appName}` : appBrand.appName
}

export async function loadAppBrandSettings(): Promise<GlobalSettings> {
  return appBrandSettingsResource.load()
}

function stringValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function syncDocumentBrand(appIcon: string): void {
  let icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!icon) {
    icon = document.createElement('link')
    icon.rel = 'icon'
    document.head.appendChild(icon)
  }
  icon.href = appIcon
  icon.type = appIcon.endsWith('.svg') ? 'image/svg+xml' : ''
}
