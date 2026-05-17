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

export function normalizeAppBrand(settings: Pick<GlobalSettings, 'appName' | 'appIcon'>): AppBrandSettings {
  return {
    appName: stringValue(settings.appName, defaultAppBrand.appName),
    appIcon: stringValue(settings.appIcon, defaultAppBrand.appIcon)
  }
}

export function applyAppBrand(settings: Pick<GlobalSettings, 'appName' | 'appIcon'>): AppBrandSettings {
  const next = normalizeAppBrand(settings)
  appBrand.appName = next.appName
  appBrand.appIcon = next.appIcon
  syncDocumentBrand(next)
  return next
}

export async function loadAppBrandSettings(): Promise<GlobalSettings> {
  const settings = await api.settings.public()
  applyAppBrand(settings)
  return settings
}

export async function loadGlobalBrandSettings(): Promise<GlobalSettings> {
  const settings = await api.settings.public()
  applyAppBrand(settings)
  return settings
}

function stringValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function syncDocumentBrand(brand: AppBrandSettings): void {
  document.title = brand.appName

  let icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!icon) {
    icon = document.createElement('link')
    icon.rel = 'icon'
    document.head.appendChild(icon)
  }
  icon.href = brand.appIcon
  icon.type = brand.appIcon.endsWith('.svg') ? 'image/svg+xml' : ''
}
