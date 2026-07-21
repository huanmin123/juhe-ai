import {
  ackModelCatalogSnapshotRebuildRequestAsync,
  findPendingModelCatalogSnapshotRebuildRequestAsync,
  listPendingModelCatalogSnapshotRebuildRequestsAsync,
  type ModelCatalogSnapshotRebuildRequest,
  type ModelCatalogSnapshotRebuildScope
} from '../../storage/model-catalog-snapshot-rebuild.repository.js'
import {
  rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync,
  rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync
} from './published-model-catalog.service.js'

export interface ModelCatalogSnapshotReconcileDependencies {
  listPendingRequests: () => Promise<ModelCatalogSnapshotRebuildRequest[]>
  findPendingRequest: (input: { scope: ModelCatalogSnapshotRebuildScope; systemAccountId?: string }) => Promise<ModelCatalogSnapshotRebuildRequest | undefined>
  ackRequest: (request: Pick<ModelCatalogSnapshotRebuildRequest, 'scope' | 'systemAccountId' | 'generation'>) => Promise<boolean>
  rebuildAll: () => Promise<unknown>
  rebuildPersonal: (systemAccountId: string) => Promise<unknown>
}

export interface ModelCatalogSnapshotReconcileResult {
  scope: ModelCatalogSnapshotRebuildScope
  systemAccountId?: string
  generation: number
  acknowledged: boolean
}

export interface ModelCatalogSnapshotReconcileScanResult {
  capturedCount: number
  rebuildCount: number
  acknowledgedCount: number
  retainedCount: number
  failedCount: number
}

interface SettledReconcileResult {
  result?: ModelCatalogSnapshotReconcileResult
  rebuildSucceeded: boolean
}

const defaultDependencies: ModelCatalogSnapshotReconcileDependencies = {
  listPendingRequests: listPendingModelCatalogSnapshotRebuildRequestsAsync,
  findPendingRequest: findPendingModelCatalogSnapshotRebuildRequestAsync,
  ackRequest: ackModelCatalogSnapshotRebuildRequestAsync,
  rebuildAll: () => rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync().then(() => undefined),
  rebuildPersonal: (systemAccountId) => rebuildPublishedModelCatalogSnapshotsForSystemAccountAsync(systemAccountId).then(() => undefined)
}

export class ModelCatalogSnapshotReconcileService {
  private readonly inFlight = new Map<string, {
    generation: number
    operation: Promise<ModelCatalogSnapshotReconcileResult>
  }>()

  public constructor(private readonly dependencies: ModelCatalogSnapshotReconcileDependencies = defaultDependencies) {}

  public async reconcileScopeAsync(input: {
    scope: ModelCatalogSnapshotRebuildScope
    systemAccountId?: string
  }): Promise<ModelCatalogSnapshotReconcileResult | undefined> {
    const request = await this.dependencies.findPendingRequest(input)
    return request ? this.reconcileCapturedRequestAsync(request) : undefined
  }

  public async reconcileDirtyOnceAsync(): Promise<ModelCatalogSnapshotReconcileScanResult> {
    const captured = await this.dependencies.listPendingRequests()
    const allRequests = captured.filter((request) => request.scope === 'all')
    const personalRequests = captured.filter((request) => request.scope === 'personal')
    const results: SettledReconcileResult[] = []

    for (const request of allRequests) {
      results.push(await this.reconcileCapturedRequestSettledAsync(request))
    }
    results.push(...await mapWithConcurrency(personalRequests, 4, (request) => this.reconcileCapturedRequestSettledAsync(request)))

    const acknowledgedCount = results.filter((outcome) => outcome.result?.acknowledged === true).length
    const failedCount = results.filter((outcome) => outcome.result === undefined).length
    return {
      capturedCount: captured.length,
      rebuildCount: results.filter((outcome) => outcome.rebuildSucceeded).length,
      acknowledgedCount,
      retainedCount: captured.length - acknowledgedCount,
      failedCount
    }
  }

  private async reconcileCapturedRequestSettledAsync(request: ModelCatalogSnapshotRebuildRequest): Promise<SettledReconcileResult> {
    try {
      return { result: await this.reconcileCapturedRequestAsync(request), rebuildSucceeded: true }
    } catch (error) {
      return { rebuildSucceeded: error instanceof ModelCatalogSnapshotAckError }
    }
  }

  private reconcileCapturedRequestAsync(request: ModelCatalogSnapshotRebuildRequest): Promise<ModelCatalogSnapshotReconcileResult> {
    return this.runSingleflightAsync(request, () => this.rebuildAndAckAsync(request))
  }

  private runSingleflightAsync(
    request: ModelCatalogSnapshotRebuildRequest,
    operationFactory: () => Promise<ModelCatalogSnapshotReconcileResult>
  ): Promise<ModelCatalogSnapshotReconcileResult> {
    const key = requestKey(request)
    const current = this.inFlight.get(key)
    if (current?.generation === request.generation) return current.operation
    if (current) {
      return current.operation
        .catch(() => undefined)
        .then(() => this.runSingleflightAsync(request, operationFactory))
    }

    const operation = operationFactory()
    this.inFlight.set(key, { generation: request.generation, operation })
    void operation.finally(() => {
      if (this.inFlight.get(key)?.operation === operation) this.inFlight.delete(key)
    }).catch(() => undefined)
    return operation
  }

  private async rebuildAndAckAsync(request: ModelCatalogSnapshotRebuildRequest): Promise<ModelCatalogSnapshotReconcileResult> {
    if (request.scope === 'all') {
      await this.dependencies.rebuildAll()
    } else {
      if (!request.systemAccountId) throw new Error('模型目录快照 personal 重建请求缺少 systemAccountId')
      await this.dependencies.rebuildPersonal(request.systemAccountId)
    }
    let acknowledged: boolean
    try {
      acknowledged = await this.dependencies.ackRequest(request)
    } catch (cause) {
      throw new ModelCatalogSnapshotAckError(cause)
    }
    return {
      scope: request.scope,
      ...(request.systemAccountId ? { systemAccountId: request.systemAccountId } : {}),
      generation: request.generation,
      acknowledged
    }
  }
}

class ModelCatalogSnapshotAckError extends Error {
  public constructor(cause: unknown) {
    super('模型目录快照重建成功，但 dirty generation 确认失败', { cause })
  }
}

export const modelCatalogSnapshotReconcileService = new ModelCatalogSnapshotReconcileService()

export function reconcileModelCatalogSnapshotScopeAsync(input: {
  scope: ModelCatalogSnapshotRebuildScope
  systemAccountId?: string
}): Promise<ModelCatalogSnapshotReconcileResult | undefined> {
  return modelCatalogSnapshotReconcileService.reconcileScopeAsync(input)
}

export function reconcileDirtyModelCatalogSnapshotsOnceAsync(): Promise<ModelCatalogSnapshotReconcileScanResult> {
  return modelCatalogSnapshotReconcileService.reconcileDirtyOnceAsync()
}

async function mapWithConcurrency<T, R>(items: T[], concurrency: number, operation: (item: T) => Promise<R>): Promise<R[]> {
  const results: R[] = []
  let nextIndex = 0
  const worker = async (): Promise<void> => {
    while (nextIndex < items.length) {
      const index = nextIndex
      nextIndex += 1
      results[index] = await operation(items[index] as T)
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, () => worker()))
  return results
}

function requestKey(request: Pick<ModelCatalogSnapshotRebuildRequest, 'scope' | 'systemAccountId'>): string {
  return request.scope === 'all' ? 'all' : `personal:${request.systemAccountId ?? ''}`
}
