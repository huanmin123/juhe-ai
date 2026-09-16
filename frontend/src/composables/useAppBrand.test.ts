import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import type { GlobalSettings } from '@/types/domain'
import {
  appBrand,
  applyAppBrand,
  createAppBrandSettingsResource,
  defaultAppBrand,
  loadAppBrandSettings,
  normalizeAppBrand,
  syncDocumentTitle,
  type AppBrandSettingsResource
} from './useAppBrand'

vi.mock('@/api/client', () => ({
  api: {
    settings: {
      public: vi.fn()
    }
  }
}))

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

beforeEach(() => {
  vi.clearAllMocks()
  document.title = ''
  document.querySelectorAll('link[rel="icon"]').forEach((link) => link.remove())
})

describe('normalizeAppBrand', () => {
  it('合法值透传并去除空白', () => {
    expect(normalizeAppBrand({ appName: ' 新名称 ', appIcon: ' /icon.svg ' })).toEqual({ appName: '新名称', appIcon: '/icon.svg' })
  })

  it('空白或非字符串回退默认品牌', () => {
    expect(normalizeAppBrand({ appName: '', appIcon: '   ' })).toEqual(defaultAppBrand)
    expect(normalizeAppBrand({} as GlobalSettings)).toEqual(defaultAppBrand)
  })
})

describe('createAppBrandSettingsResource', () => {
  it('load 拉取数据、提交回调并缓存', async () => {
    const value = { appName: 'A', appIcon: '/a.svg' } as GlobalSettings
    const fetch = vi.fn().mockResolvedValue(value)
    const commit = vi.fn()
    const resource: AppBrandSettingsResource<GlobalSettings> = createAppBrandSettingsResource({ fetch, commit })
    await expect(resource.load()).resolves.toBe(value)
    await expect(resource.load()).resolves.toBe(value)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(commit).toHaveBeenCalledTimes(1)
    expect(commit).toHaveBeenCalledWith(value)
  })

  it('并发 load 共享同一个 in-flight 请求', async () => {
    const gate = deferred<GlobalSettings>()
    const fetch = vi.fn().mockReturnValue(gate.promise)
    const commit = vi.fn()
    const resource = createAppBrandSettingsResource({ fetch, commit })
    const first = resource.load()
    const second = resource.load()
    gate.resolve({ appName: 'B', appIcon: '/b.svg' })
    await expect(first).resolves.toEqual({ appName: 'B', appIcon: '/b.svg' })
    await expect(second).resolves.toEqual({ appName: 'B', appIcon: '/b.svg' })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('set 立即提交并使缓存生效，旧请求结果不再提交', async () => {
    const gate = deferred<GlobalSettings>()
    const fetch = vi.fn().mockReturnValue(gate.promise)
    const commit = vi.fn()
    const resource = createAppBrandSettingsResource({ fetch, commit })
    const stale = resource.load()
    const manual = { appName: '手动', appIcon: '/m.svg' } as GlobalSettings
    expect(resource.set(manual)).toBe(manual)
    expect(commit).toHaveBeenCalledTimes(1)
    // 旧请求晚于 set 返回：不提交、不覆盖缓存。
    gate.resolve({ appName: '过期', appIcon: '/old.svg' })
    await expect(stale).resolves.toEqual(manual)
    expect(commit).toHaveBeenCalledTimes(1)
    await expect(resource.load()).resolves.toBe(manual)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('clear 后重新拉取', async () => {
    const first = { appName: '一', appIcon: '/1.svg' } as GlobalSettings
    const second = { appName: '二', appIcon: '/2.svg' } as GlobalSettings
    const fetch = vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(second)
    const commit = vi.fn()
    const resource = createAppBrandSettingsResource({ fetch, commit })
    await expect(resource.load()).resolves.toBe(first)
    resource.clear()
    await expect(resource.load()).resolves.toBe(second)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(commit).toHaveBeenLastCalledWith(second)
  })
})

describe('全局品牌状态', () => {
  it('loadAppBrandSettings 拉取远端设置并应用到文档', async () => {
    const settings = { appName: '远端名称', appIcon: '/remote.svg' } as GlobalSettings
    vi.mocked(api.settings.public).mockResolvedValue(settings)
    await expect(loadAppBrandSettings()).resolves.toBe(settings)
    expect(appBrand.appName).toBe('远端名称')
    expect(document.title).toBe('远端名称')
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(icon?.getAttribute('href')).toBe('/remote.svg')
    expect(icon?.type).toBe('image/svg+xml')
  })

  it('loadAppBrandSettings 复用模块级缓存不重复请求', async () => {
    vi.mocked(api.settings.public).mockResolvedValue({ appName: '不应到达', appIcon: '/x.svg' } as GlobalSettings)
    // 上一用例已加载并缓存，这里两次 load 都应命中缓存。
    await loadAppBrandSettings()
    await loadAppBrandSettings()
    expect(api.settings.public).not.toHaveBeenCalled()
  })

  it('applyAppBrand 覆盖全局品牌并更新图标', async () => {
    const applied = applyAppBrand({ appName: '本地名称', appIcon: '/local.png' })
    expect(applied).toEqual({ appName: '本地名称', appIcon: '/local.png' })
    expect(appBrand.appName).toBe('本地名称')
    expect(appBrand.appIcon).toBe('/local.png')
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(icon?.getAttribute('href')).toBe('/local.png')
    expect(icon?.type).toBe('')
  })

  it('syncDocumentTitle 拼接页面标题', () => {
    syncDocumentTitle('页面')
    expect(document.title).toBe('页面 - 本地名称')
    syncDocumentTitle('本地名称')
    expect(document.title).toBe('本地名称')
    syncDocumentTitle('   ')
    expect(document.title).toBe('本地名称')
    syncDocumentTitle()
    expect(document.title).toBe('本地名称')
  })
})
