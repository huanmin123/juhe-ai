import { runtimeConfig } from '../../config/runtime.js'
import {
  createModelCheckItems,
  createModelCheckItemsAsync,
  createModelCheckRun,
  createModelCheckRunAsync,
  finishModelCheckRun,
  finishModelCheckRunAsync,
  type ModelCheckItemCreateInput,
  type ModelCheckRunCreateInput,
  type ModelCheckRunFinishInput
} from '../../storage/model-checks.repository.js'
import { sqliteWriterBoundaryStrictModeEnabled } from '../../storage/database.js'
import type { ModelCheckItemSummary, ModelCheckRunSummary } from '../../domain/types.js'
import { requestBackgroundWorkerDatasetWrite } from './background-ipc.js'

export type BackgroundDatasetWriteOperation =
  | {
    type: 'create_model_check_run'
    input: ModelCheckRunCreateInput
  }
  | {
    type: 'create_model_check_items'
    runId: string
    items: ModelCheckItemCreateInput[]
  }
  | {
    type: 'finish_model_check_run'
    runId: string
    input: ModelCheckRunFinishInput
  }

export type BackgroundDatasetWriteOperationResult<T extends BackgroundDatasetWriteOperation = BackgroundDatasetWriteOperation> =
  T extends { type: 'create_model_check_run' } ? ModelCheckRunSummary :
  T extends { type: 'create_model_check_items' } ? ModelCheckItemSummary[] :
  T extends { type: 'finish_model_check_run' } ? ModelCheckRunSummary | undefined :
  unknown

export async function requestDatasetWriter<T extends BackgroundDatasetWriteOperation>(
  operation: T,
  timeoutMs = 30_000
): Promise<BackgroundDatasetWriteOperationResult<T>> {
  if (currentProcessOwnsDatasetWriter()) {
    return await handleDatasetWriteOperation(operation) as BackgroundDatasetWriteOperationResult<T>
  }
  const result = await requestBackgroundWorkerDatasetWrite(operation, timeoutMs)
  if (result === undefined) {
    throw new Error(`dataset-writer 不可用，无法执行数据集写操作：${operation.type}`)
  }
  return result as BackgroundDatasetWriteOperationResult<T>
}

export async function handleDatasetWriteOperation(operation: BackgroundDatasetWriteOperation): Promise<unknown> {
  switch (operation.type) {
    case 'create_model_check_run':
      return runtimeConfig.databaseDriver === 'postgres'
        ? await createModelCheckRunAsync(operation.input)
        : createModelCheckRun(operation.input)
    case 'create_model_check_items':
      return runtimeConfig.databaseDriver === 'postgres'
        ? await createModelCheckItemsAsync(operation.runId, operation.items)
        : createModelCheckItems(operation.runId, operation.items)
    case 'finish_model_check_run':
      return runtimeConfig.databaseDriver === 'postgres'
        ? await finishModelCheckRunAsync(operation.runId, operation.input)
        : finishModelCheckRun(operation.runId, operation.input)
    default:
      return assertNever(operation)
  }
}

function currentProcessOwnsDatasetWriter(): boolean {
  return !sqliteWriterBoundaryStrictModeEnabled()
    || (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'ingest-worker')
}

function assertNever(value: never): never {
  throw new Error(`未知数据集写操作：${JSON.stringify(value)}`)
}
