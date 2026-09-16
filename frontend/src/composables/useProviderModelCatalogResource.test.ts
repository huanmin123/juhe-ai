import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import type { ProviderModelPricing } from '@/types/domain'
import { loadProviderModelCatalogResource } from './useProviderModelCatalogResource'

vi.mock('@/api/client', () => ({
  api: {
    providers: {
      models: vi.fn()
    }
  }
}))

beforeEach(() => {
  vi.clearAllMocks()
})

describe('loadProviderModelCatalogResource', () => {
  it('trim 供应商 code 后请求并返回结果', async () => {
    const pricing = [{ id: 'm1' }] as unknown as ProviderModelPricing[]
    vi.mocked(api.providers.models).mockResolvedValue(pricing)
    await expect(loadProviderModelCatalogResource({ providerCode: ' gpt ', isManagementView: true }))
      .resolves.toBe(pricing)
    expect(api.providers.models).toHaveBeenCalledWith('gpt', undefined)
  })

  it('透传查询参数的副本', async () => {
    vi.mocked(api.providers.models).mockResolvedValue([])
    const query = { includeUnpriced: true }
    await loadProviderModelCatalogResource({ providerCode: 'glm', isManagementView: false, query })
    expect(api.providers.models).toHaveBeenCalledWith('glm', query)
    expect(vi.mocked(api.providers.models).mock.calls[0][1]).not.toBe(query)
  })

  it('无查询参数时传 undefined', async () => {
    vi.mocked(api.providers.models).mockResolvedValue([])
    await loadProviderModelCatalogResource({ providerCode: 'openai', isManagementView: true })
    expect(api.providers.models).toHaveBeenCalledWith('openai', undefined)
  })
})
