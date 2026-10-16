import { beforeEach, describe, expect, it, vi } from 'vitest'

const modelOptionProviderCodes = vi.fn()
vi.mock('@/api/client', () => ({
  api: {
    providers: {
      modelOptionProviderCodes: (...args: unknown[]) => modelOptionProviderCodes(...args)
    }
  }
}))

// v8-D 媒体域新建默认供应商收敛：域内在售目录供应商集合的预取缓存契约。
// 缓存按 域 × 查看面 × 管理面目标系统账户 区分；失败不缓存负结果；
// 打开新建弹窗的收敛只同步读取（cachedMediaDomainModelAvailability）。

describe('mediaDomainModelAvailability：域内在售目录供应商预取缓存（v8-D）', () => {
  beforeEach(() => {
    modelOptionProviderCodes.mockReset()
    vi.resetModules()
  })

  async function loadModule() {
    return await import('./mediaDomainModelAvailability')
  }

  it('预取聚合 providerCodes 为集合并缓存；同步读取未就绪返回 undefined', async () => {
    modelOptionProviderCodes.mockResolvedValue({ providerCodes: ['glm', 'gpt', 'glm', ' '] })
    const mod = await loadModule()
    const scope = { capabilityDomain: 'video' as const, isManagementView: false }
    expect(mod.cachedMediaDomainModelAvailability(scope)).toBeUndefined()
    const codes = await mod.prefetchMediaDomainModelAvailability(scope)
    expect([...codes].sort()).toEqual(['glm', 'gpt'])
    expect(mod.cachedMediaDomainModelAvailability(scope)).toBe(codes)
    expect(modelOptionProviderCodes).toHaveBeenCalledWith({ modes: ['video'] })
  })

  it('缓存 key 按域区分；管理面带 systemAccountId，用户面不带', async () => {
    modelOptionProviderCodes.mockResolvedValue({ providerCodes: ['glm'] })
    const mod = await loadModule()
    await mod.prefetchMediaDomainModelAvailability({ capabilityDomain: 'video', isManagementView: true, systemAccountId: 'acct-1' })
    expect(modelOptionProviderCodes).toHaveBeenLastCalledWith({ modes: ['video'], systemAccountId: 'acct-1' })
    await mod.prefetchMediaDomainModelAvailability({ capabilityDomain: 'audio', isManagementView: true, systemAccountId: 'acct-1' })
    expect(modelOptionProviderCodes).toHaveBeenLastCalledWith({ modes: ['audio'], systemAccountId: 'acct-1' })
    await mod.prefetchMediaDomainModelAvailability({ capabilityDomain: 'video', isManagementView: false, systemAccountId: 'acct-1' })
    expect(modelOptionProviderCodes).toHaveBeenLastCalledWith({ modes: ['video'] })
  })

  it('预取失败不缓存负结果，可重试；同 key 并发请求去重', async () => {
    modelOptionProviderCodes.mockRejectedValueOnce(new Error('boom'))
    const mod = await loadModule()
    const scope = { capabilityDomain: 'video' as const, isManagementView: false }
    await expect(mod.prefetchMediaDomainModelAvailability(scope)).rejects.toThrow('boom')
    expect(mod.cachedMediaDomainModelAvailability(scope)).toBeUndefined()
    modelOptionProviderCodes.mockResolvedValue({ providerCodes: ['glm'] })
    const pending = mod.prefetchMediaDomainModelAvailability(scope)
    const retry = mod.prefetchMediaDomainModelAvailability(scope)
    expect(retry).toBe(pending)
    await expect(pending).resolves.toBeInstanceOf(Set)
    expect(modelOptionProviderCodes).toHaveBeenCalledTimes(2)
  })
})
