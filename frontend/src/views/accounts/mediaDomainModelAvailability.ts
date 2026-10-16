import { api } from '@/api/client'
import { DOMAIN_MODEL_MODES, type AccountCapabilityDomain } from './accountCapabilityDomains'

/**
 * v8-D 媒体域新建默认供应商收敛的预取缓存：域内「在售目录供应商集合」
 * （GET /providers/models/options/provider-codes，口径与 /models/options 一致）。
 * 列表页挂载/切域时预取一次；打开新建弹窗时同步读取，保证默认供应商收敛
 * 不依赖打开后的异步改选（避免默认值跳变与用户已输入被重置）。预取未就绪
 * 或失败时调用方回退既有默认值，行为退回 v8-D 之前的状态。
 */

interface AvailabilityScopeInput {
  isManagementView: boolean
  systemAccountId?: string
}

const cache = new Map<string, ReadonlySet<string>>()
const inflight = new Map<string, Promise<ReadonlySet<string>>>()

function availabilityCacheKey(capabilityDomain: AccountCapabilityDomain, scope: AvailabilityScopeInput): string {
  // 目录聚合按查看面与（管理面）目标系统账户区分：personal custom 行的可见
  // 范围随 systemAccountId 变化，缓存 key 必须含两者。
  return JSON.stringify([
    capabilityDomain,
    scope.isManagementView ? 'management' : 'self',
    scope.isManagementView ? scope.systemAccountId?.trim() ?? '' : ''
  ])
}

function availabilityRequestParams(scope: AvailabilityScopeInput): { systemAccountId?: string } {
  // 用户面不传 systemAccountId（服务端钉到调用者身份）；管理面携带所选系统
  // 账户，personal custom 行口径与模型候选加载一致。
  if (scope.isManagementView && scope.systemAccountId?.trim()) {
    return { systemAccountId: scope.systemAccountId.trim() }
  }
  return {}
}

export function prefetchMediaDomainModelAvailability(input: {
  capabilityDomain: AccountCapabilityDomain
  isManagementView: boolean
  systemAccountId?: string
}): Promise<ReadonlySet<string>> {
  const key = availabilityCacheKey(input.capabilityDomain, input)
  const cached = cache.get(key)
  if (cached) return Promise.resolve(cached)
  const existing = inflight.get(key)
  if (existing) return existing
  const promise = api.providers.modelOptionProviderCodes({
    modes: [...DOMAIN_MODEL_MODES[input.capabilityDomain]],
    ...availabilityRequestParams(input)
  }).then((payload) => {
    const codes = new Set<string>()
    for (const code of payload.providerCodes ?? []) {
      const trimmed = code.trim()
      if (trimmed) codes.add(trimmed)
    }
    cache.set(key, codes)
    return codes as ReadonlySet<string>
  }).finally(() => {
    inflight.delete(key)
  })
  inflight.set(key, promise)
  return promise
}

/** 同步读取已就绪的预取结果；未就绪返回 undefined，调用方保持既有默认供应商。 */
export function cachedMediaDomainModelAvailability(input: {
  capabilityDomain: AccountCapabilityDomain
  isManagementView: boolean
  systemAccountId?: string
}): ReadonlySet<string> | undefined {
  return cache.get(availabilityCacheKey(input.capabilityDomain, input))
}
