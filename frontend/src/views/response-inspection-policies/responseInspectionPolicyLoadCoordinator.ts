import type {
  ResponseInspectionPolicyDetail,
  ResponseInspectionPolicyListResult,
  ResponseInspectionPolicyProviderOption
} from '@/types/domain'

export interface ResponseInspectionPolicyModalIntent {
  revision: number
  policyId?: string
}

interface ResponseInspectionPolicyLoaders {
  list: (signal: AbortSignal) => Promise<ResponseInspectionPolicyListResult>
  detail: (policyId: string, signal: AbortSignal) => Promise<ResponseInspectionPolicyDetail>
  providerOptions: (signal: AbortSignal) => Promise<ResponseInspectionPolicyProviderOption[]>
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
  let optionsRequest: Promise<ResponseInspectionPolicyProviderOption[] | undefined> | undefined
  let loadedOptions: ResponseInspectionPolicyProviderOption[] | undefined
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
    intent: ResponseInspectionPolicyModalIntent
  ): Promise<ResponseInspectionPolicyProviderOption[] | undefined> {
    if (!isCurrentModalIntent(intent)) return undefined
    if (loadedOptions) return loadedOptions
    if (!optionsRequest) optionsRequest = requestProviderOptions(intent)
    try {
      const result = await optionsRequest
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
    invalidateProviderOptions()
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
    invalidateProviderOptions()
    listController = undefined
    detailController = undefined
  }

  function requestProviderOptions(
    intent: ResponseInspectionPolicyModalIntent
  ): Promise<ResponseInspectionPolicyProviderOption[] | undefined> {
    optionsController?.abort()
    const revision = ++optionsRevision
    const controller = new AbortController()
    optionsController = controller
    const request = (async () => {
      try {
        const result = await loaders.providerOptions(controller.signal)
        if (!isCurrentOptionsRequest(revision, controller) || !isCurrentModalIntent(intent)) return undefined
        loadedOptions = result
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

  function invalidateProviderOptions(): void {
    optionsRevision += 1
    optionsController?.abort()
    optionsController = undefined
    optionsRequest = undefined
    loadedOptions = undefined
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
    cancelModalIntent,
    dispose,
    isCurrentModalIntent,
    loadDetail,
    loadList,
    loadProviderOptions
  }
}

export function isCanceledRequest(error: unknown): boolean {
  const candidate = error as { code?: unknown; name?: unknown } | undefined
  return candidate?.code === 'ERR_CANCELED'
    || candidate?.name === 'CanceledError'
    || candidate?.name === 'AbortError'
}
