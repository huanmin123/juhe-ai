import type {
  ResponseInspectionPolicyDetail,
  ResponseInspectionPolicyListResult,
  ResponseInspectionPolicyProtocolCode,
  ResponseInspectionPolicyProviderOption,
  ResponseInspectionPolicyScopeType
} from '@/types/domain'

export interface ResponseInspectionPolicyModalIntent {
  revision: number
  policyId?: string
}

export interface ResponseInspectionPolicyProviderOptionsQuery {
  protocolCode: ResponseInspectionPolicyProtocolCode
  scopeType: ResponseInspectionPolicyScopeType
  keyword?: string
}

interface ResponseInspectionPolicyLoaders {
  list: (signal: AbortSignal) => Promise<ResponseInspectionPolicyListResult>
  detail: (policyId: string, signal: AbortSignal) => Promise<ResponseInspectionPolicyDetail>
  providerOptions: (
    query: ResponseInspectionPolicyProviderOptionsQuery,
    signal: AbortSignal
  ) => Promise<ResponseInspectionPolicyProviderOption[]>
}

export function createResponseInspectionPolicyLoadCoordinator(loaders: ResponseInspectionPolicyLoaders) {
  let disposed = false
  let listRevision = 0
  let listController: AbortController | undefined
  let detailRevision = 0
  let detailPolicyId = ''
  let detailController: AbortController | undefined
  let optionsRevision = 0
  let optionsController: AbortController | undefined
  let optionsRequest: {
    key: string
    promise: Promise<ResponseInspectionPolicyProviderOption[] | undefined>
  } | undefined
  const loadedOptions = new Map<string, ResponseInspectionPolicyProviderOption[]>()
  let modalRevision = 0
  let modalPolicyId: string | undefined

  async function loadList(): Promise<ResponseInspectionPolicyListResult | undefined> {
    listController?.abort()
    const revision = ++listRevision
    const controller = new AbortController()
    listController = controller
    try {
      const result = await loaders.list(controller.signal)
      return isCurrentListRequest(revision, controller) ? result : undefined
    } catch (error) {
      if (isCanceledRequest(error) || !isCurrentListRequest(revision, controller)) return undefined
      throw error
    } finally {
      if (isCurrentListRequest(revision, controller)) listController = undefined
    }
  }

  async function loadDetail(
    intent: ResponseInspectionPolicyModalIntent,
    policyId: string
  ): Promise<ResponseInspectionPolicyDetail | undefined> {
    if (!isCurrentModalIntent(intent, policyId)) return undefined
    detailController?.abort()
    const revision = ++detailRevision
    const controller = new AbortController()
    detailController = controller
    detailPolicyId = policyId
    try {
      const detail = await loaders.detail(policyId, controller.signal)
      return isCurrentDetailRequest(revision, policyId, controller) && isCurrentModalIntent(intent, policyId)
        ? detail
        : undefined
    } catch (error) {
      if (
        isCanceledRequest(error)
        || !isCurrentDetailRequest(revision, policyId, controller)
        || !isCurrentModalIntent(intent, policyId)
      ) return undefined
      throw error
    } finally {
      if (isCurrentDetailRequest(revision, policyId, controller)) detailController = undefined
    }
  }

  async function loadProviderOptions(
    intent: ResponseInspectionPolicyModalIntent,
    input: ResponseInspectionPolicyProviderOptionsQuery
  ): Promise<ResponseInspectionPolicyProviderOption[] | undefined> {
    if (!isCurrentModalIntent(intent)) return undefined
    const query = normalizeProviderOptionsQuery(input)
    const key = providerOptionsCacheKey(query)
    const cached = loadedOptions.get(key)
    if (cached) return cached
    if (optionsRequest?.key !== key) {
      cancelProviderOptionsRequest()
      optionsRequest = { key, promise: requestProviderOptions(intent, query, key) }
    }
    try {
      const result = await optionsRequest.promise
      return result && isCurrentModalIntent(intent) ? result : undefined
    } catch (error) {
      if (!isCurrentModalIntent(intent)) return undefined
      throw error
    }
  }

  function beginModalIntent(policyId?: string): ResponseInspectionPolicyModalIntent {
    detailController?.abort()
    detailController = undefined
    detailPolicyId = ''
    detailRevision += 1
    cancelProviderOptionsRequest()
    modalRevision += 1
    modalPolicyId = policyId
    return { revision: modalRevision, policyId }
  }

  function cancelModalIntent(): void {
    beginModalIntent()
  }

  function isCurrentModalIntent(intent: ResponseInspectionPolicyModalIntent, policyId = intent.policyId): boolean {
    return !disposed
      && intent.revision === modalRevision
      && modalPolicyId === policyId
  }

  function dispose(): void {
    disposed = true
    listRevision += 1
    detailRevision += 1
    modalRevision += 1
    listController?.abort()
    detailController?.abort()
    cancelProviderOptionsRequest()
    loadedOptions.clear()
    listController = undefined
    detailController = undefined
  }

  function requestProviderOptions(
    intent: ResponseInspectionPolicyModalIntent,
    query: ResponseInspectionPolicyProviderOptionsQuery,
    key: string
  ): Promise<ResponseInspectionPolicyProviderOption[] | undefined> {
    const revision = ++optionsRevision
    const controller = new AbortController()
    optionsController = controller
    const request = (async () => {
      try {
        const result = await loaders.providerOptions(query, controller.signal)
        if (!isCurrentOptionsRequest(revision, controller) || !isCurrentModalIntent(intent)) return undefined
        loadedOptions.set(key, result)
        return result
      } catch (error) {
        if (
          isCanceledRequest(error)
          || !isCurrentOptionsRequest(revision, controller)
          || !isCurrentModalIntent(intent)
        ) return undefined
        throw error
      } finally {
        if (isCurrentOptionsRequest(revision, controller)) {
          optionsController = undefined
          optionsRequest = undefined
        }
      }
    })()
    return request
  }

  function cancelProviderOptionsRequest(): void {
    optionsRevision += 1
    optionsController?.abort()
    optionsController = undefined
    optionsRequest = undefined
  }

  function isCurrentListRequest(revision: number, controller: AbortController): boolean {
    return !disposed && revision === listRevision && listController === controller
  }

  function isCurrentDetailRequest(revision: number, policyId: string, controller: AbortController): boolean {
    return !disposed
      && revision === detailRevision
      && detailPolicyId === policyId
      && detailController === controller
  }

  function isCurrentOptionsRequest(revision: number, controller: AbortController): boolean {
    return !disposed && revision === optionsRevision && optionsController === controller
  }

  return {
    beginModalIntent,
    cancelProviderOptionsRequest,
    cancelModalIntent,
    dispose,
    isCurrentModalIntent,
    loadDetail,
    loadList,
    loadProviderOptions
  }
}

function normalizeProviderOptionsQuery(
  input: ResponseInspectionPolicyProviderOptionsQuery
): ResponseInspectionPolicyProviderOptionsQuery {
  const keyword = input.keyword?.trim()
  return {
    protocolCode: input.protocolCode,
    scopeType: input.scopeType,
    ...(keyword ? { keyword } : {})
  }
}

function providerOptionsCacheKey(input: ResponseInspectionPolicyProviderOptionsQuery): string {
  return `${input.protocolCode}\u0000${input.scopeType}\u0000${input.keyword?.toLocaleLowerCase() ?? ''}`
}

export function isCanceledRequest(error: unknown): boolean {
  const candidate = error as { code?: unknown; name?: unknown } | undefined
  return candidate?.code === 'ERR_CANCELED'
    || candidate?.name === 'CanceledError'
    || candidate?.name === 'AbortError'
}
